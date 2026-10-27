//go:build darwin

package config

import "os"

// RutaConfig es la ubicación por defecto del archivo de configuración en macOS.
// Mismo path que la variante Unix genérica (ver config_unix.go) — /etc sigue siendo
// el directorio correcto para config de servicio de sistema — pero los permisos con
// los que se crea cambian (ver PermsDirectorio/PermsArchivo).
const RutaConfig = "/etc/copime-agent/config.json"

// PermsDirectorio y PermsArchivo en macOS son más abiertos que en Linux/Windows a
// propósito: el servicio se registra como LaunchAgent (corre como el usuario con
// sesión gráfica, no como root — ver install.sh y com.conasinf.copime-agent.plist),
// así que el proceso que necesita LEER config.json en cada arranque ya no es root.
//
// config.json solo guarda server_url, device_id, key (credencial Basic Auth de ESTE
// dispositivo, alcance limitado a enviar su propio inventario — ver sender.go) y
// metadata de versión/intervalo: ningún dato personal ni credencial reutilizable
// fuera de ese dispositivo. Windows ya trata esta misma llave como no crítica — su
// config vive en %ProgramData%, con las ACL por defecto de Windows ahí (lectura para
// usuarios estándar, no solo administradores/LocalSystem) — ver config_windows.go.
// Mismo criterio aplicado aquí: 0755/0644 para que cualquier usuario local pueda
// leerla (requisito real para que el LaunchAgent arranque), nunca escribirla.
const (
	PermsDirectorio os.FileMode = 0755
	PermsArchivo    os.FileMode = 0644
)
