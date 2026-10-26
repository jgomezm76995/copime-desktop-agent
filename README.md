# COPIME Desktop Agent (CDA)

Agente de inventario de hardware y software para el ERP COPIME. Reemplaza GLPI Agent (Perl, terceros) con una implementación propia en Go — multiplataforma y diseñada para ejecutarse como daemon/servicio de sistema.

## Características

- **Enrolamiento seguro** con código de un solo uso
- **Inventario macOS** vía herramientas nativas (`system_profiler`, `sysctl`, `ifconfig`, `ioreg`) — sin dependencias externas, solo stdlib de Go
- **Inventario Windows** vía WMI (`Get-CimInstance` como subproceso de PowerShell) para hardware/SO/CPU/RAM/disco/red, y lectura directa del registro de desinstalación para software instalado — única dependencia externa: `golang.org/x/sys` (extensión oficial de Go para Windows, sin cgo)
- **Sync periódico** con intervalo configurable por el servidor
- **Backoff exponencial** ante errores de red (1 min → 4 horas máximo)
- **Respeto de `Retry-After`** en respuestas 429
- **Daemon launchd** para inicio automático en macOS
- **Servicio de Windows (SCM)** con inicio automático, registrado vía `golang.org/x/sys/windows/svc`

## Compilación

```bash
# macOS Apple Silicon (arm64)
GOOS=darwin GOARCH=arm64 go build -o cda-darwin-arm64 ./cmd/agent/

# macOS Intel (amd64)
GOOS=darwin GOARCH=amd64 go build -o cda-darwin-amd64 ./cmd/agent/

# Windows (amd64 / arm64) — compila igual desde macOS/Linux, sin cgo ni toolchain de Windows
GOOS=windows GOARCH=amd64 go build -o cda-windows-amd64.exe ./cmd/agent/
GOOS=windows GOARCH=arm64 go build -o cda-windows-arm64.exe ./cmd/agent/

# Con versión embebida
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=0.1.0" -o cda-darwin-arm64 ./cmd/agent/
```

## Instalación en macOS

```bash
# 1. Compilar el binario para la arquitectura del equipo destino
GOOS=darwin GOARCH=arm64 go build -o install/cda-darwin-arm64 ./cmd/agent/

# 2. Ejecutar el instalador (requiere sudo)
cd install/
sudo ./install.sh --server https://conasinf.copime.net --enroll-code XXXX-XXXX
```

El instalador:
1. Copia el binario a `/usr/local/bin/cda`
2. Crea `/etc/copime-agent/` con permisos 700
3. Ejecuta el enrolamiento y guarda `config.json`
4. Instala el plist en `/Library/LaunchDaemons/`
5. Registra el servicio con `launchctl bootstrap`

## Instalación en Windows

```bat
:: 1. Compilar el binario (desde cualquier plataforma) y copiarlo junto a install.bat
go build -o install\cda.exe .\cmd\agent\

:: 2. Ejecutar el instalador como Administrador
cd install
install.bat --server https://conasinf.copime.net --enroll-code XXXX-XXXX
```

El instalador:
1. Copia el binario a `%ProgramFiles%\COPIME\copime-agent\cda.exe`
2. Crea `%ProgramData%\COPIME\copime-agent\`
3. Ejecuta el enrolamiento y guarda `config.json`
4. Registra el servicio `COPIMEDesktopAgent` en el Service Control Manager (inicio automático) y lo arranca

## Desinstalación

```bash
# macOS
sudo ./install/install.sh --uninstall
```

```bat
:: Windows (como Administrador)
install.bat --uninstall
```

## Uso manual

```bash
# Primer uso — enrolamiento
cda --server https://conasinf.copime.net --enroll-code XXXX-XXXX

# Modo daemon (para launchd)
cda

# Un solo ciclo de inventario (debug)
cda --once

# Versión
cda --version

# Configuración personalizada
cda --config /ruta/alternativa/config.json --once

# Windows: registrar/eliminar el servicio de Windows manualmente (install.bat ya lo hace)
cda.exe --install-service
cda.exe --uninstall-service
```

## Configuración

- macOS/Linux: `/etc/copime-agent/config.json` (permisos 0600, dueño root)
- Windows: `%ProgramData%\COPIME\copime-agent\config.json`

```json
{
  "server_url": "https://conasinf.copime.net",
  "device_id": "cad_xxxx",
  "key": "<llave-base64url>",
  "sync_interval_hours": 24,
  "agent_version": "0.1.0"
}
```

## Estructura del repositorio

```
copime-desktop-agent/
  go.mod                                     Módulo Go
  cmd/agent/
    main.go                                 Punto de entrada, flags CLI, lógica de modos
    service_windows.go                      Registro/ejecución como servicio de Windows (build tag: windows)
    service_other.go                        Stubs de lo anterior en macOS/Linux (build tag: !windows)
  internal/
    config/
      config.go                             Lectura/escritura de config.json (multiplataforma)
      config_unix.go                        RutaConfig por defecto en macOS/Linux (build tag: !windows)
      config_windows.go                     RutaConfig por defecto en Windows (build tag: windows)
    enroll/enroll.go                         Flujo de enrolamiento con el servidor
    inventory/
      collector.go                           Interfaz Collector + tipos de datos
      darwin.go                              Implementación macOS (build tag: darwin)
      windows.go                             Implementación Windows (build tag: windows)
    sender/sender.go                         HTTP client con auth Basic, retry y backoff
  install/
    install.sh                               Instalador macOS (launchd)
    com.conasinf.copime-agent.plist          Plist de launchd
    install.bat                              Instalador Windows (SCM)
  README.md
```

## Logs

- macOS: el daemon escribe en `/var/log/copime-agent.log` (vía StandardOutPath del plist).
- Windows: el servicio escribe en `%ProgramData%\COPIME\copime-agent\copime-agent.log` (el SCM no redirige stdout/stderr por sí solo).

```
[CDA] 2024/01/15 10:30:00 Configuración cargada. DeviceID: cad_abc123 — Servidor: https://conasinf.copime.net
[CDA] 2024/01/15 10:30:00 Iniciando recolección de inventario del sistema...
[CDA] 2024/01/15 10:30:05 Inventario recolectado. Enviando al servidor...
[CDA] 2024/01/15 10:30:06 Inventario enviado exitosamente. Próximo sync en 24 horas (según servidor).
```

## Requisitos

- Go 1.22+
- macOS 13+ (Ventura) recomendado; compatible con macOS 12+ — sin dependencias externas, solo stdlib de Go
- Windows 10/11, Server 2016+ — requiere PowerShell (incluido por defecto); única dependencia externa del módulo: `golang.org/x/sys`

## Protocolo del servidor

### Enrolamiento
```
POST /api/v1/support-assets/agent/enroll
{"code": "XXXX-XXXX"}
→ {"ok": true, "data": {"device_id": "cad_xxx", "key": "<base64url>"}}
```

### Inventario / heartbeat
```
POST /api/v1/support-assets/agent/inventory
Authorization: Basic base64(device_id:key)
→ {"status": "ok", "sync_interval_hours": 24}
```

## Licencia

Uso interno — CONASINF / COPIME ERP. No distribuir.
