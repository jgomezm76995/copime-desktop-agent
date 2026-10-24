// Punto de entrada del COPIME Desktop Agent (CDA).
// Soporta dos modos de operación:
//   - Enrolamiento: cda --server URL --enroll-code XXXX
//   - Sync daemon:  cda            (usa la configuración ya guardada)
//   - Un ciclo:     cda --once     (útil para debug)
//   - Versión:      cda --version
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jgomezm76995/copime-desktop-agent/internal/config"
	"github.com/jgomezm76995/copime-desktop-agent/internal/enroll"
	"github.com/jgomezm76995/copime-desktop-agent/internal/inventory"
	"github.com/jgomezm76995/copime-desktop-agent/internal/sender"
)

// version es la versión del agente. Se puede sobreescribir en tiempo de compilación
// con: go build -ldflags "-X main.version=0.1.0"
var version = "0.1.0"

func main() {
	// --- Definición de flags ---
	flagServer := flag.String("server", "", "URL base del servidor COPIME (p.ej. https://conasinf.copime.net)")
	flagEnrollCode := flag.String("enroll-code", "", "Código de enrolamiento de un solo uso")
	flagConfig := flag.String("config", config.RutaConfig, "Ruta al archivo de configuración")
	flagOnce := flag.Bool("once", false, "Ejecutar un solo ciclo de inventario y salir (para depuración)")
	flagVersion := flag.Bool("version", false, "Mostrar la versión del agente y salir")

	flag.Parse()

	// Configurar el logger con timestamps.
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[CDA] ")

	// --- Flag --version ---
	if *flagVersion {
		fmt.Printf("COPIME Desktop Agent v%s\n", version)
		os.Exit(0)
	}

	// --- Determinar modo de operación ---
	cfg, errLeerConfig := config.Leer(*flagConfig)

	if errLeerConfig != nil || (cfg != nil && !cfg.EstaCompleta()) {
		// Modo enrolamiento: la configuración no existe o está incompleta.
		ejecutarEnrolamiento(*flagServer, *flagEnrollCode, *flagConfig)
		return
	}

	// Modo sync: configuración válida disponible.
	log.Printf("Configuración cargada. DeviceID: %s — Servidor: %s", cfg.DeviceID, cfg.ServerURL)

	if *flagOnce {
		log.Println("Modo --once: ejecutando un único ciclo de inventario.")
		if err := cicloInventario(cfg); err != nil {
			log.Fatalf("Error en ciclo de inventario: %v", err)
		}
		log.Println("Ciclo completado. Saliendo.")
		return
	}

	// Modo daemon: bucle infinito respetando sync_interval_hours.
	log.Printf("Iniciando modo daemon. Intervalo de sync: %d horas.", cfg.SyncIntervalHours)
	ejecutarDaemon(cfg, *flagConfig)
}

// ejecutarEnrolamiento maneja el flujo de primer uso del agente.
func ejecutarEnrolamiento(serverURL, codigo, rutaConfig string) {
	if serverURL == "" {
		log.Fatal("ERROR: Se requiere --server para el enrolamiento (p.ej. --server https://conasinf.copime.net)")
	}
	if codigo == "" {
		log.Fatal("ERROR: Se requiere --enroll-code para el enrolamiento")
	}

	log.Printf("Iniciando enrolamiento con el servidor: %s", serverURL)

	resultado, err := enroll.Ejecutar(serverURL, codigo)
	if err != nil {
		log.Fatalf("ERROR en enrolamiento: %v", err)
	}

	log.Printf("Enrolamiento exitoso. DeviceID asignado: %s", resultado.DeviceID)

	// Guardar la configuración.
	cfg := &config.Config{
		ServerURL:         serverURL,
		DeviceID:          resultado.DeviceID,
		Key:               resultado.Key,
		SyncIntervalHours: 24, // valor por defecto hasta el primer sync
		AgentVersion:      version,
	}

	if err := config.Guardar(rutaConfig, cfg); err != nil {
		log.Fatalf("ERROR al guardar configuración: %v", err)
	}

	log.Printf("Configuración guardada en %s", rutaConfig)
	log.Println("El agente está listo. Ejecuta sin flags para iniciar el sync daemon.")
}

// ejecutarDaemon corre el ciclo de inventario indefinidamente.
func ejecutarDaemon(cfg *config.Config, rutaConfig string) {
	for {
		if err := cicloInventario(cfg); err != nil {
			// Si la credencial fue revocada, no tiene sentido continuar.
			if _, esRevocada := err.(*sender.ErrCredencialRevocada); esRevocada {
				log.Printf("CREDENCIAL REVOCADA: %v", err)
				log.Println("El agente se detiene. La configuración se conserva para diagnóstico.")
				os.Exit(1)
			}
			// Para otros errores, el backoff ya fue manejado dentro de EnviarInventario.
			log.Printf("Error en ciclo de inventario: %v", err)
		}

		// Releer la configuración para respetar un sync_interval_hours actualizado.
		cfgActualizada, err := config.Leer(rutaConfig)
		if err == nil && cfgActualizada.SyncIntervalHours > 0 {
			cfg = cfgActualizada
		}

		intervalo := time.Duration(cfg.SyncIntervalHours) * time.Hour
		if intervalo <= 0 {
			intervalo = 24 * time.Hour
		}

		log.Printf("Próximo sync en %v.", intervalo)
		time.Sleep(intervalo)
	}
}

// cicloInventario recolecta el inventario del sistema y lo envía al servidor.
func cicloInventario(cfg *config.Config) error {
	log.Println("Iniciando recolección de inventario del sistema...")

	collector := inventory.NuevoCollector()

	deviceID, err := collector.ObtenerDeviceID()
	if err != nil {
		return fmt.Errorf("obtener device ID de hardware: %w", err)
	}

	inv, err := collector.RecolectarInventario()
	if err != nil {
		return fmt.Errorf("recolectar inventario: %w", err)
	}

	log.Printf("Inventario recolectado. Enviando al servidor %s...", cfg.ServerURL)

	senderCfg := sender.Config{
		ServerURL: cfg.ServerURL,
		DeviceID:  cfg.DeviceID,
		Key:       cfg.Key,
	}

	respuesta, err := sender.EnviarInventario(senderCfg, deviceID, inv)
	if err != nil {
		return err
	}

	log.Printf("Inventario enviado exitosamente. Próximo sync en %d horas (según servidor).",
		respuesta.SyncIntervalHours)

	// Actualizar el intervalo de sync si el servidor lo cambió.
	if respuesta.SyncIntervalHours > 0 {
		cfg.SyncIntervalHours = respuesta.SyncIntervalHours
	}

	return nil
}
