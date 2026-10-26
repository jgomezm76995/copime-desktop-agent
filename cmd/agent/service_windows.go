//go:build windows

// Registro e integración del COPIME Desktop Agent con el Service Control Manager
// (SCM) de Windows — equivalente Windows de install.sh/launchd en macOS. Usa
// golang.org/x/sys/windows/svc (+ svc/mgr), la extensión oficial de Go para
// Windows, sin cgo: es el enfoque idiomático para correr como servicio real en Go,
// análogo en espíritu a KeepAlive+RunAtLoad del plist de macOS.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/jgomezm76995/copime-desktop-agent/internal/config"
)

// nombreServicioWindows es el nombre interno del servicio en el SCM (el que usan
// `sc.exe`/services.msc para identificarlo), distinto de su DisplayName legible.
const nombreServicioWindows = "COPIMEDesktopAgent"

// esServicioWindows indica si el proceso actual fue arrancado por el SCM (en vez
// de ejecutado interactivamente desde una consola, p.ej. durante el enrolamiento).
func esServicioWindows() bool {
	es, err := svc.IsWindowsService()
	return err == nil && es
}

// correrComoServicioWindows cede el control al despachador de servicio de Windows:
// bloquea hasta que el SCM pide detener el servicio. Debe llamarse únicamente
// cuando esServicioWindows() ya confirmó que el proceso corre bajo el SCM.
func correrComoServicioWindows(cfg *config.Config, rutaConfig string) {
	handler := &copimeServiceHandler{cfg: cfg, rutaConfig: rutaConfig}
	if err := svc.Run(nombreServicioWindows, handler); err != nil {
		log.Fatalf("ERROR al ejecutar como servicio de Windows: %v", err)
	}
}

// copimeServiceHandler implementa svc.Handler delegando el trabajo real al mismo
// bucle de daemon (ejecutarDaemon, en main.go) que usan macOS/Linux bajo
// launchd/systemd.
type copimeServiceHandler struct {
	cfg        *config.Config
	rutaConfig string
}

func (h *copimeServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const aceptados = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	// El SCM no conecta el stdout/stderr del servicio a ninguna consola ni archivo
	// — a diferencia de StandardOutPath/StandardErrorPath del plist de launchd en
	// macOS, sin esto los logs del daemon se perderían por completo al correr
	// como servicio. Se redirige el logger estándar a un archivo junto a
	// config.json (no crítico: si falla, el servicio sigue operando sin logs).
	if archivoLog, err := abrirArchivoLogServicio(h.rutaConfig); err == nil {
		defer archivoLog.Close()
		log.SetOutput(archivoLog)
	} else {
		log.Printf("[ADVERTENCIA] No se pudo abrir el archivo de log del servicio: %v", err)
	}

	// ejecutarDaemon no tiene mecanismo de cancelación interno: es el mismo bucle
	// infinito que corre bajo launchd/systemd en las otras plataformas, donde el
	// supervisor de sistema controla el ciclo de vida deteniendo el PROCESO
	// completo, no pidiéndole al bucle que termine limpio. Aquí se reproduce lo
	// mismo: al recibir Stop/Shutdown, Execute retorna sin esperar a la goroutine,
	// lo que termina main() y por tanto todo el proceso (y con él, el daemon).
	go ejecutarDaemon(h.cfg, h.rutaConfig)

	changes <- svc.Status{State: svc.Running, Accepts: aceptados}

	for req := range r {
		switch req.Cmd {
		case svc.Interrogate:
			changes <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}

	return false, 0
}

// instalarServicioWindows registra el agente en el SCM con inicio automático y lo
// arranca de inmediato. Es idempotente: si el servicio ya existe (reinstalación),
// solo se asegura de que esté iniciado en vez de fallar.
func instalarServicioWindows() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("obtener ruta del ejecutable: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("resolver ruta absoluta del ejecutable: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("conectar con el Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	if existente, err := m.OpenService(nombreServicioWindows); err == nil {
		existente.Close()
		return arrancarServicioExistente(m)
	}

	cfgServicio := mgr.Config{
		DisplayName:  "COPIME Desktop Agent",
		Description:  "Agente de inventario de hardware y software del ERP COPIME (reemplazo de GLPI Agent).",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}

	s, err := m.CreateService(nombreServicioWindows, exePath, cfgServicio)
	if err != nil {
		return fmt.Errorf("crear servicio de Windows: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("iniciar servicio de Windows: %w", err)
	}

	return nil
}

func arrancarServicioExistente(m *mgr.Mgr) error {
	s, err := m.OpenService(nombreServicioWindows)
	if err != nil {
		return fmt.Errorf("reabrir servicio existente: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return fmt.Errorf("iniciar servicio existente: %w", err)
	}
	return nil
}

// desinstalarServicioWindows detiene (si está corriendo) y elimina el registro del
// servicio del SCM. No elimina el binario ni la configuración — eso lo hace
// install.bat, igual que install.sh hace con el plist en macOS.
func desinstalarServicioWindows() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("conectar con el Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(nombreServicioWindows)
	if err != nil {
		return fmt.Errorf("el servicio %q no está instalado: %w", nombreServicioWindows, err)
	}
	defer s.Close()

	if estado, err := s.Query(); err == nil && estado.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			log.Printf("[ADVERTENCIA] No se pudo detener el servicio antes de eliminarlo: %v", err)
		}
		esperarEstadoServicio(s, svc.Stopped, 10*time.Second)
	}

	return s.Delete()
}

// abrirArchivoLogServicio abre (creando si falta) el archivo de log del servicio,
// colocado junto al archivo de configuración.
func abrirArchivoLogServicio(rutaConfig string) (*os.File, error) {
	ruta := filepath.Join(filepath.Dir(rutaConfig), "copime-agent.log")
	return os.OpenFile(ruta, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

func esperarEstadoServicio(s *mgr.Service, deseado svc.State, limite time.Duration) {
	plazo := time.Now().Add(limite)
	for time.Now().Before(plazo) {
		estado, err := s.Query()
		if err != nil || estado.State == deseado {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}
