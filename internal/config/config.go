// Package config maneja la lectura y escritura del archivo de configuración
// local del COPIME Desktop Agent en /etc/copime-agent/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RutaConfig es la ubicación por defecto del archivo de configuración.
const RutaConfig = "/etc/copime-agent/config.json"

// Config representa la configuración persistente del agente.
type Config struct {
	// ServerURL es la URL base del servidor COPIME (p.ej. https://conasinf.copime.net).
	ServerURL string `json:"server_url"`

	// DeviceID es el identificador único del dispositivo asignado por el servidor tras el enrolamiento.
	DeviceID string `json:"device_id"`

	// Key es la llave de autenticación en texto claro asignada por el servidor.
	Key string `json:"key"`

	// SyncIntervalHours es el intervalo de sincronización indicado por el servidor.
	SyncIntervalHours int `json:"sync_interval_hours"`

	// AgentVersion es la versión del agente instalado.
	AgentVersion string `json:"agent_version"`
}

// EstaCompleta indica si la configuración tiene los campos mínimos para operar en modo sync.
func (c *Config) EstaCompleta() bool {
	return c.ServerURL != "" && c.DeviceID != "" && c.Key != ""
}

// Leer carga la configuración desde la ruta indicada.
// Retorna error si el archivo no existe o tiene formato inválido.
func Leer(ruta string) (*Config, error) {
	datos, err := os.ReadFile(ruta)
	if err != nil {
		return nil, fmt.Errorf("leer config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(datos, &cfg); err != nil {
		return nil, fmt.Errorf("parsear config: %w", err)
	}

	return &cfg, nil
}

// Guardar persiste la configuración en la ruta indicada con permisos 0600.
// Crea los directorios padres si no existen (con permisos 0700).
func Guardar(ruta string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(ruta), 0700); err != nil {
		return fmt.Errorf("crear directorio de config: %w", err)
	}

	datos, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar config: %w", err)
	}

	// Escribir con permisos 0600 (solo root puede leer).
	if err := os.WriteFile(ruta, datos, 0600); err != nil {
		return fmt.Errorf("escribir config: %w", err)
	}

	return nil
}
