// Package enroll implementa el flujo de enrolamiento del COPIME Desktop Agent
// con el servidor COPIME. Envía un código de un solo uso y recibe las credenciales
// permanentes (device_id + key) que se persisten en la configuración local.
package enroll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// peticionEnrolamiento es el cuerpo JSON enviado al servidor.
type peticionEnrolamiento struct {
	Code string `json:"code"`
}

// respuestaEnrolamiento modela la respuesta exitosa del servidor.
type respuestaEnrolamiento struct {
	Ok   bool `json:"ok"`
	Data struct {
		DeviceID string `json:"device_id"`
		Key      string `json:"key"`
	} `json:"data"`
}

// ResultadoEnrolamiento contiene las credenciales obtenidas tras el enrolamiento.
type ResultadoEnrolamiento struct {
	DeviceID string
	Key      string
}

// Ejecutar realiza el enrolamiento contra el servidor COPIME.
// Recibe la URL base del servidor y el código de un solo uso.
// Retorna las credenciales permanentes o un error descriptivo.
func Ejecutar(serverURL, codigo string) (*ResultadoEnrolamiento, error) {
	url := serverURL + "/api/v1/support-assets/agent/enroll"

	cuerpo, err := json.Marshal(peticionEnrolamiento{Code: codigo})
	if err != nil {
		return nil, fmt.Errorf("serializar petición de enrolamiento: %w", err)
	}

	cliente := &http.Client{Timeout: 30 * time.Second}

	resp, err := cliente.Post(url, "application/json", bytes.NewReader(cuerpo))
	if err != nil {
		return nil, fmt.Errorf("conectar con el servidor para enrolamiento: %w", err)
	}
	defer resp.Body.Close()

	cuerpoRespuesta, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("leer respuesta del servidor: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// continúa abajo
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("código de enrolamiento inválido o vencido (401)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("demasiadas solicitudes al servidor, intenta más tarde (429)")
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("servidor no disponible en este momento (503)")
	default:
		return nil, fmt.Errorf("respuesta inesperada del servidor: %d — %s", resp.StatusCode, string(cuerpoRespuesta))
	}

	var respuesta respuestaEnrolamiento
	if err := json.Unmarshal(cuerpoRespuesta, &respuesta); err != nil {
		return nil, fmt.Errorf("parsear respuesta de enrolamiento: %w", err)
	}

	if !respuesta.Ok {
		return nil, fmt.Errorf("el servidor rechazó el enrolamiento (ok=false)")
	}
	if respuesta.Data.DeviceID == "" || respuesta.Data.Key == "" {
		return nil, fmt.Errorf("respuesta de enrolamiento incompleta: falta device_id o key")
	}

	return &ResultadoEnrolamiento{
		DeviceID: respuesta.Data.DeviceID,
		Key:      respuesta.Data.Key,
	}, nil
}
