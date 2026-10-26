//go:build !windows

package config

// RutaConfig es la ubicación por defecto del archivo de configuración en sistemas
// tipo Unix (macOS, Linux): directorio estándar de configuración de servicios de
// sistema, solo accesible con privilegios de root (ver permisos 0700/0600 en
// Guardar).
const RutaConfig = "/etc/copime-agent/config.json"
