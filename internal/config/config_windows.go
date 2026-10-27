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

// PermsDirectorio y PermsArchivo son los modos que Guardar (en config.go) pasa a
// os.MkdirAll/os.WriteFile. Go los aplica solo parcialmente en Windows (el control
// de acceso real ahí es ACL de NTFS, no bits POSIX) — se mantienen con el mismo
// valor que ya tenía Guardar hardcodeado antes de este cambio, únicamente para que
// Guardar tenga una sola firma multiplataforma; el control de acceso real en esta
// plataforma es el de %ProgramData% (ver comentario de RutaConfig arriba).
const (
	PermsDirectorio os.FileMode = 0700
	PermsArchivo    os.FileMode = 0600
)

func construirRutaConfig() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "COPIME", "copime-agent", "config.json")
}
