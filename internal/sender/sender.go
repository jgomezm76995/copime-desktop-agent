// Package sender implementa el cliente HTTP que envía el inventario al servidor COPIME.
// Incluye autenticación Basic (device_id:key), reintentos con backoff exponencial
// y respeto del header Retry-After en respuestas 429.
package sender

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jgomezm76995/copime-desktop-agent/internal/inventory"
)

// peticionInventario es el cuerpo JSON del endpoint de inventario.
type peticionInventario struct {
	Action   string               `json:"action"`
	DeviceID string               `json:"deviceid"`
	Content  inventory.Inventario `json:"content"`
}

// RespuestaInventario modela la respuesta exitosa del servidor.
type RespuestaInventario struct {
	Status            string `json:"status"`
	SyncIntervalHours int    `json:"sync_interval_hours"`
}

// ErrCredencialRevocada indica que el servidor rechazó las credenciales (401).
// El agente debe detener el sync al recibir este error.
type ErrCredencialRevocada struct {
	Mensaje string
}

func (e *ErrCredencialRevocada) Error() string {
	return e.Mensaje
}

// Config agrupa los datos necesarios para autenticar y enviar el inventario.
type Config struct {
	ServerURL string
	DeviceID  string
	Key       string
}

// EnviarInventario envía el inventario al servidor con reintentos y backoff exponencial.
// Retorna la respuesta del servidor o un error.
// Si el error es *ErrCredencialRevocada, el agente debe detener el sync permanentemente.
func EnviarInventario(cfg Config, deviceID string, inv *inventory.Inventario) (*RespuestaInventario, error) {
	const (
		maxIntentos    = 8
		backoffInicial = 1 * time.Minute
		backoffMaximo  = 4 * time.Hour
	)

	var ultimoError error

	for intento := 0; intento < maxIntentos; intento++ {
		respuesta, err := enviarUnaVez(cfg, deviceID, inv)

		if err == nil {
			return respuesta, nil
		}

		// Error de credencial revocada — no reintentar.
		if _, esRevocada := err.(*ErrCredencialRevocada); esRevocada {
			return nil, err
		}

		// Error con Retry-After (429).
		if retryAfter, ok := extraerRetryAfter(err); ok {
			log.Printf("[ADVERTENCIA] Rate limit recibido (429). Esperando %v antes de reintentar...", retryAfter)
			time.Sleep(retryAfter)
			ultimoError = err
			continue
		}

		ultimoError = err

		if intento < maxIntentos-1 {
			espera := calcularBackoff(intento, backoffInicial, backoffMaximo)
			log.Printf("[ADVERTENCIA] Error al enviar inventario (intento %d/%d): %v. Reintentando en %v...",
				intento+1, maxIntentos, err, espera)
			time.Sleep(espera)
		}
	}

	return nil, fmt.Errorf("todos los intentos fallaron. Último error: %w", ultimoError)
}

// enviarUnaVez realiza un único intento de envío del inventario.
func enviarUnaVez(cfg Config, deviceID string, inv *inventory.Inventario) (*RespuestaInventario, error) {
	url := cfg.ServerURL + "/api/v1/support-assets/agent/inventory"

	peticion := peticionInventario{
		Action:   "inventory",
		DeviceID: deviceID,
		Content:  *inv,
	}

	cuerpo, err := json.Marshal(peticion)
	if err != nil {
		return nil, fmt.Errorf("serializar inventario: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(cuerpo))
	if err != nil {
		return nil, fmt.Errorf("crear petición HTTP: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+credencialesBasic(cfg.DeviceID, cfg.Key))

	cliente := &http.Client{Timeout: 60 * time.Second}
	resp, err := cliente.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enviar petición al servidor: %w", err)
	}
	defer resp.Body.Close()

	cuerpoRespuesta, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("leer respuesta del servidor: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// Continúa abajo.
	case http.StatusUnauthorized:
		return nil, &ErrCredencialRevocada{
			Mensaje: "credencial revocada por el servidor (401) — el agente debe detenerse",
		}
	case http.StatusTooManyRequests:
		retryAfter := resp.Header.Get("Retry-After")
		return nil, &errRateLimited{retryAfterHeader: retryAfter}
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("servidor no disponible (503): %s", string(cuerpoRespuesta))
	default:
		return nil, fmt.Errorf("respuesta inesperada del servidor: %d — %s",
			resp.StatusCode, string(cuerpoRespuesta))
	}

	var resultado RespuestaInventario
	if err := json.Unmarshal(cuerpoRespuesta, &resultado); err != nil {
		return nil, fmt.Errorf("parsear respuesta del servidor: %w", err)
	}

	return &resultado, nil
}

// credencialesBasic construye el valor del header Authorization para HTTP Basic Auth.
func credencialesBasic(deviceID, key string) string {
	return base64.StdEncoding.EncodeToString([]byte(deviceID + ":" + key))
}

// calcularBackoff calcula el tiempo de espera exponencial con jitter simple.
func calcularBackoff(intento int, inicial, maximo time.Duration) time.Duration {
	espera := time.Duration(float64(inicial) * math.Pow(2, float64(intento)))
	if espera > maximo {
		espera = maximo
	}
	return espera
}

// errRateLimited representa un error 429 con su header Retry-After.
type errRateLimited struct {
	retryAfterHeader string
}

func (e *errRateLimited) Error() string {
	return fmt.Sprintf("rate limit (429), Retry-After: %s", e.retryAfterHeader)
}

// extraerRetryAfter extrae el valor de Retry-After de un error errRateLimited.
func extraerRetryAfter(err error) (time.Duration, bool) {
	var rl *errRateLimited
	if !isErrRateLimited(err, &rl) {
		return 0, false
	}

	if rl.retryAfterHeader == "" {
		return 60 * time.Second, true
	}

	// El header puede ser segundos (número) o una fecha HTTP.
	if segundos, err := strconv.Atoi(strings.TrimSpace(rl.retryAfterHeader)); err == nil {
		return time.Duration(segundos) * time.Second, true
	}

	// Intentar parsear como fecha HTTP.
	if t, err := http.ParseTime(rl.retryAfterHeader); err == nil {
		espera := time.Until(t)
		if espera < 0 {
			espera = 0
		}
		return espera, true
	}

	return 60 * time.Second, true
}

func isErrRateLimited(err error, target **errRateLimited) bool {
	if rl, ok := err.(*errRateLimited); ok {
		*target = rl
		return true
	}
	return false
}
