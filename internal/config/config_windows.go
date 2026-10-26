//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// RutaConfig es la ubicación por defecto del archivo de configuración en Windows.
//
// Windows no tiene un equivalente directo a /etc: el directorio convencional para
// datos de aplicaciones/servicios compartidos entre todos los usuarios de la máquina
// es %ProgramData% (resuelto en tiempo de ejecución, normalmente C:\ProgramData, y no
// hardcodeado porque puede estar en otra unidad en instalaciones no estándar). El
// agente corre como servicio de Windows bajo LocalSystem (ver internal/service), que
// tiene acceso de lectura/escritura ahí sin configuración adicional de permisos — a
// diferencia de /etc en Unix, %ProgramData% no tiene un modo 0700 equivalente por
// archivo; la carpeta queda con los permisos por defecto de Windows para esa ruta
// (administradores y LocalSystem), suficiente porque solo guarda una llave de
// dispositivo, no credenciales de usuario final.
//
// Si %ProgramData% no está definido (no debería ocurrir en una instalación real),
// se usa la ruta por defecto documentada por Microsoft como respaldo.
var RutaConfig = construirRutaConfig()

func construirRutaConfig() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "COPIME", "copime-agent", "config.json")
}
