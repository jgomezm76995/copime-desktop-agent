# COPIME Desktop Agent (CDA)

Agente de inventario de hardware y software para el ERP COPIME. Reemplaza GLPI Agent (Perl, terceros) con una implementación propia en Go — sin dependencias externas, multiplataforma y diseñada para ejecutarse como daemon de sistema.

## Características

- **Enrolamiento seguro** con código de un solo uso
- **Inventario macOS** vía herramientas nativas (`system_profiler`, `sysctl`, `ifconfig`, `ioreg`)
- **Sync periódico** con intervalo configurable por el servidor
- **Backoff exponencial** ante errores de red (1 min → 4 horas máximo)
- **Respeto de `Retry-After`** en respuestas 429
- **Daemon launchd** para inicio automático en macOS

## Compilación

```bash
# macOS Apple Silicon (arm64)
GOOS=darwin GOARCH=arm64 go build -o cda-darwin-arm64 ./cmd/agent/

# macOS Intel (amd64)
GOOS=darwin GOARCH=amd64 go build -o cda-darwin-amd64 ./cmd/agent/

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

## Desinstalación

```bash
sudo ./install/install.sh --uninstall
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
```

## Configuración

Archivo: `/etc/copime-agent/config.json` (permisos 0600, dueño root)

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
  cmd/agent/main.go                          Punto de entrada, flags CLI, lógica de modos
  internal/
    config/config.go                         Lectura/escritura de config.json
    enroll/enroll.go                         Flujo de enrolamiento con el servidor
    inventory/
      collector.go                           Interfaz Collector + tipos de datos
      darwin.go                              Implementación macOS (build tag: darwin)
    sender/sender.go                         HTTP client con auth Basic, retry y backoff
  install/
    install.sh                               Script de instalación/desinstalación
    com.conasinf.copime-agent.plist          Plist de launchd
  README.md
```

## Logs

El daemon escribe en `/var/log/copime-agent.log`:

```
[CDA] 2024/01/15 10:30:00 Configuración cargada. DeviceID: cad_abc123 — Servidor: https://conasinf.copime.net
[CDA] 2024/01/15 10:30:00 Iniciando recolección de inventario del sistema...
[CDA] 2024/01/15 10:30:05 Inventario recolectado. Enviando al servidor...
[CDA] 2024/01/15 10:30:06 Inventario enviado exitosamente. Próximo sync en 24 horas (según servidor).
```

## Requisitos

- Go 1.22+
- macOS 13+ (Ventura) recomendado; compatible con macOS 12+
- Sin dependencias externas — solo stdlib de Go

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
