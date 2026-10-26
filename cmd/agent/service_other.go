//go:build !windows

// Contraparte no-Windows de service_windows.go: el registro como servicio del
// Service Control Manager no existe fuera de Windows (macOS/Linux usan
// launchd/systemd vía install.sh, no un flag del propio binario), así que estas
// funciones son stubs inalcanzables en la práctica — esServicioWindows() siempre
// retorna false, por lo que main.go nunca llama a correrComoServicioWindows() en
// estas plataformas, y los flags --install-service/--uninstall-service fallan con
// un mensaje claro en vez de compilar solo en Windows.
package main

import (
	"fmt"

	"github.com/jgomezm76995/copime-desktop-agent/internal/config"
)

func esServicioWindows() bool {
	return false
}

func correrComoServicioWindows(cfg *config.Config, rutaConfig string) {
	// No alcanzable: esServicioWindows() siempre retorna false en esta plataforma.
}

func instalarServicioWindows() error {
	return fmt.Errorf("--install-service solo está disponible en Windows")
}

func desinstalarServicioWindows() error {
	return fmt.Errorf("--uninstall-service solo está disponible en Windows")
}
