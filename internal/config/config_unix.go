//go:build !windows && !darwin

package config

import "os"

// RutaConfig es la ubicación por defecto del archivo de configuración en sistemas
// tipo Unix (Linux): directorio estándar de configuración de servicios de sistema,
// solo accesible con privilegios de root (ver PermsDirectorio/PermsArchivo abajo).
//
// macOS tiene su propia variante (ver config_darwin.go): mismo path, pero permisos
// más abiertos porque el servicio corre como LaunchAgent del usuario, no como
// LaunchDaemon de root.
const RutaConfig = "/etc/copime-agent/config.json"

// PermsDirectorio y PermsArchivo controlan los permisos con los que Guardar (en
// config.go) crea el directorio de configuración y escribe config.json. En esta
// plataforma el servicio sigue corriendo como root, así que se mantiene el modo
// restringido original (solo root puede leer la llave de autenticación).
const (
	PermsDirectorio os.FileMode = 0700
	PermsArchivo    os.FileMode = 0600
)
