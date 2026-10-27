#!/usr/bin/env bash
# install.sh — Instala o desinstala el COPIME Desktop Agent (CDA) en macOS.
#
# Uso:
#   sudo ./install.sh --server URL --enroll-code CODE
#   sudo ./install.sh --uninstall
#
# El script requiere permisos de root para:
#   - Copiar el binario a /usr/local/bin/
#   - Crear /etc/copime-agent/ (modo 755 — ver nota de LaunchAgent abajo)
#   - Instalar el plist en /Library/LaunchAgents/
#   - Crear /Library/Logs/COPIME/ (modo 755, log pre-creado en modo 666 — ver nota
#     de LaunchAgent abajo)
#   - Registrar el servicio con launchctl
#
# Nota — LaunchAgent, no LaunchDaemon: a nivel de confianza de firma, launchd
# ACEPTA la firma ad-hoc del binario (ver codesign más abajo) cuando el servicio se
# registra como LaunchAgent (dominio "gui/<uid>", corre como un usuario normal), pero
# la RECHAZA con "Bootstrap failed: 5: Input/output error" cuando se registra como
# LaunchDaemon de sistema (dominio "system", corre como root) — verificado en una Mac
# real reproduciendo ambos casos con el mismo binario. La alternativa real sería
# firmar con Developer ID y notarizar, no disponible todavía. Por eso el servicio
# corre como LaunchAgent del usuario con sesión gráfica activa, aunque este script
# siga necesitando sudo para instalar archivos en rutas de sistema. Consecuencia
# directa: el proceso que lee config.json y escribe el log en cada arranque ya no es
# root, así que ambas rutas (config y log) necesitan permisos legibles/escribibles
# por un usuario normal — ver PermsDirectorio/PermsArchivo en internal/config/
# config_darwin.go y la creación de LOG_DIR más abajo.

set -euo pipefail

BINARY_NAME="cda"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/copime-agent"
LOG_DIR="/Library/Logs/COPIME"
PLIST_PATH="/Library/LaunchAgents/com.conasinf.copime-agent.plist"
PLIST_LABEL="com.conasinf.copime-agent"

# --- Detectar el usuario con sesión gráfica activa (dueño de /dev/console) ---
# Un LaunchAgent en /Library/LaunchAgents se carga automáticamente, sin intervención
# de este script, para CUALQUIER usuario que inicie sesión gráfica de aquí en
# adelante — eso cubre el caso normal sin hacer nada más. Lo que sí necesita este
# script es activar el servicio AHORA, en la sesión ya abierta, sin esperar a que ese
# usuario cierre y vuelva a iniciar sesión; para eso hace falta el UID real de esa
# sesión (root no tiene dominio "gui/<uid>" propio).
detectar_uid_consola() {
  local usuario
  usuario="$(stat -f%Su /dev/console 2>/dev/null || true)"
  if [ -z "$usuario" ] || [ "$usuario" = "root" ]; then
    return 1
  fi
  id -u "$usuario" 2>/dev/null
}

# --- Verificar que corre como root ---
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: Este script debe ejecutarse con sudo o como root." >&2
  exit 1
fi

# --- Parsear argumentos ---
SERVER=""
ENROLL_CODE=""
UNINSTALL=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)
      SERVER="$2"
      shift 2
      ;;
    --enroll-code)
      ENROLL_CODE="$2"
      shift 2
      ;;
    --uninstall)
      UNINSTALL=true
      shift
      ;;
    *)
      echo "ERROR: Argumento desconocido: $1" >&2
      echo "Uso: sudo ./install.sh --server URL --enroll-code CODE" >&2
      echo "     sudo ./install.sh --uninstall" >&2
      exit 1
      ;;
  esac
done

# --- Modo desinstalación ---
if [ "$UNINSTALL" = true ]; then
  echo "==> Desinstalando COPIME Desktop Agent..."

  # Retirar el servicio de launchd. Es un LaunchAgent (dominio "gui/<uid>" del
  # usuario con sesión gráfica), no un LaunchDaemon de sistema — ver nota al inicio
  # del archivo — así que el bootout debe apuntar a ese dominio, no a "system".
  CONSOLE_UID="$(detectar_uid_consola || true)"
  if [ -n "$CONSOLE_UID" ] && launchctl print "gui/$CONSOLE_UID/$PLIST_LABEL" &>/dev/null; then
    echo "  Retirando servicio de launchd (gui/$CONSOLE_UID)..."
    launchctl bootout "gui/$CONSOLE_UID" "$PLIST_PATH" 2>/dev/null || true
  fi

  # Eliminar archivos del agente.
  [ -f "$PLIST_PATH" ] && rm -f "$PLIST_PATH" && echo "  Eliminado: $PLIST_PATH"
  [ -f "$INSTALL_DIR/$BINARY_NAME" ] && rm -f "$INSTALL_DIR/$BINARY_NAME" && echo "  Eliminado: $INSTALL_DIR/$BINARY_NAME"
  [ -d "$CONFIG_DIR" ] && rm -rf "$CONFIG_DIR" && echo "  Eliminado: $CONFIG_DIR"

  echo "==> Desinstalación completa."
  echo "    Nota: los logs en $LOG_DIR no se eliminan automáticamente."
  exit 0
fi

# --- Modo instalación ---
if [ -z "$SERVER" ] || [ -z "$ENROLL_CODE" ]; then
  # Sin TTY (p. ej. invocado desde un script automatizado) no hay a quién
  # preguntarle: se mantiene el mismo error de uso de siempre, en vez de
  # quedarse colgado esperando una entrada que nunca llega.
  if [ ! -t 0 ]; then
    echo "ERROR: Se requieren --server y --enroll-code para la instalación." >&2
    echo "Uso: sudo ./install.sh --server https://conasinf.copime.net --enroll-code XXXX-XXXX" >&2
    exit 1
  fi

  if [ -z "$SERVER" ]; then
    read -r -p "URL del servidor COPIME (ej. https://conasinf.copime.net): " SERVER
  fi
  if [ -z "$ENROLL_CODE" ]; then
    read -r -p "Código de enrolamiento: " ENROLL_CODE
  fi
fi

if [ -z "$SERVER" ] || [[ ! "$SERVER" =~ ^https?:// ]]; then
  echo "ERROR: La URL del servidor no puede estar vacía y debe iniciar con http:// o https://." >&2
  exit 1
fi
if [ -z "$ENROLL_CODE" ]; then
  echo "ERROR: El código de enrolamiento no puede estar vacío." >&2
  exit 1
fi

echo "==> Instalando COPIME Desktop Agent..."

# Determinar la arquitectura del sistema.
ARCH="$(uname -m)"
case "$ARCH" in
  arm64)
    BINARY_SUFFIX="darwin-arm64"
    ;;
  x86_64)
    BINARY_SUFFIX="darwin-amd64"
    ;;
  *)
    echo "ERROR: Arquitectura no soportada: $ARCH" >&2
    exit 1
    ;;
esac

BINARY_SRC="./$BINARY_NAME-$BINARY_SUFFIX"

# Verificar que el binario existe para esta arquitectura.
if [ ! -f "$BINARY_SRC" ]; then
  echo "ERROR: No se encontró el binario '$BINARY_SRC'." >&2
  echo "Compila el agente primero:" >&2
  echo "  GOOS=darwin GOARCH=$ARCH go build -o $BINARY_NAME-$BINARY_SUFFIX ./cmd/agent/" >&2
  exit 1
fi

# 1. Instalar el binario.
echo "  Instalando binario ($ARCH) en $INSTALL_DIR/$BINARY_NAME..."
install -m 755 "$BINARY_SRC" "$INSTALL_DIR/$BINARY_NAME"

# Si el .zip de release se descargó con el navegador, macOS marca cada
# archivo extraído con el atributo extendido com.apple.quarantine; Gatekeeper
# puede matar en silencio (SIGKILL) un binario quarantined que no está
# notarizado por Apple, incluso ya con firma ad-hoc — es un mecanismo
# independiente del chequeo de firma de AMFI de más abajo. Se quita solo ese
# atributo puntual, nunca todos los xattrs; `|| true` porque si el binario no
# lo trae (p. ej. compilado localmente), xattr -d falla y no debe bloquear.
if xattr -p com.apple.quarantine "$INSTALL_DIR/$BINARY_NAME" &>/dev/null; then
  echo "  Quitando atributo de cuarentena (com.apple.quarantine) del binario..."
  xattr -d com.apple.quarantine "$INSTALL_DIR/$BINARY_NAME" 2>/dev/null || true
fi

# En Apple Silicon el kernel exige al menos una firma ad-hoc para ejecutar
# cualquier binario; uno sin firmar se mata con SIGKILL en el primer exec, sin
# diálogo de Gatekeeper ni mensaje claro. `codesign` es una utilidad del
# sistema (no de Xcode) presente en cualquier Mac real; el `|| true` evita
# bloquear la instalación si un entorno atípico no la tiene disponible.
echo "  Firmando el binario con identidad ad-hoc..."
codesign --force --deep --sign - "$INSTALL_DIR/$BINARY_NAME" 2>/dev/null || true
if codesign --verify "$INSTALL_DIR/$BINARY_NAME" &>/dev/null; then
  echo "  Firma ad-hoc verificada."
else
  echo "  ADVERTENCIA: no se pudo verificar la firma ad-hoc del binario (continuando)." >&2
fi

# 2. Crear directorio de configuración. Modo 755 (no 700): el servicio corre como
#    LaunchAgent del usuario con sesión gráfica (ver nota de cabecera), así que ese
#    usuario necesita poder LEER config.json en cada arranque. config.json en sí se
#    escribe en modo 644 (ver internal/config/config_darwin.go) — contiene la llave
#    de autenticación de ESTE dispositivo (alcance limitado a su propio endpoint de
#    inventario, igual que en la instalación de Windows del mismo agente), nunca
#    credenciales de usuario final.
echo "  Creando directorio de configuración $CONFIG_DIR (modo 755)..."
install -d -m 755 "$CONFIG_DIR"

# 3. Ejecutar el enrolamiento (escribe config.json en CONFIG_DIR).
echo "  Realizando enrolamiento con el servidor $SERVER..."
if ! "$INSTALL_DIR/$BINARY_NAME" --server "$SERVER" --enroll-code "$ENROLL_CODE" --config "$CONFIG_DIR/config.json"; then
  echo "ERROR: El enrolamiento falló. Verifica el servidor y el código de enrolamiento." >&2
  exit 1
fi

# 4. Instalar el plist de launchd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLIST_SRC="$SCRIPT_DIR/com.conasinf.copime-agent.plist"

if [ ! -f "$PLIST_SRC" ]; then
  echo "ERROR: No se encontró el plist en $PLIST_SRC." >&2
  exit 1
fi

echo "  Instalando plist de launchd en $PLIST_PATH..."
install -m 644 "$PLIST_SRC" "$PLIST_PATH"

# 5. Crear el directorio de logs. El servicio corre como LaunchAgent del usuario
#    (ver nota de cabecera), así que /var/log (root:wheel, modo 755) no sirve: ese
#    usuario no puede escribir ahí y launchd no podría abrir StandardOutPath/
#    StandardErrorPath del plist. /Library/Logs es la convención real de macOS para
#    logs de aplicación compartidos entre usuarios; se deja en modo 755 y el archivo
#    de log se pre-crea en modo 666 para que cualquier usuario pueda escribirlo sin
#    abrir el directorio entero a creación de archivos arbitrarios.
echo "  Creando directorio de logs $LOG_DIR..."
install -d -m 755 "$LOG_DIR"
touch "$LOG_DIR/copime-agent.log"
chmod 666 "$LOG_DIR/copime-agent.log"

# 6. Registrar el servicio con launchd como LaunchAgent (ver nota de cabecera: un
#    LaunchDaemon de sistema falla con "Bootstrap failed: 5: Input/output error"
#    contra la firma ad-hoc del binario; un LaunchAgent no).
#
#    Colocar el plist en /Library/LaunchAgents ya es suficiente para que macOS lo
#    cargue automáticamente en cada sesión gráfica futura, de cualquier usuario, sin
#    intervención de este script. El bootstrap explícito de abajo solo activa el
#    servicio AHORA, en la sesión ya abierta, para no obligar a cerrar y volver a
#    iniciar sesión.
CONSOLE_UID="$(detectar_uid_consola || true)"
if [ -n "$CONSOLE_UID" ]; then
  echo "  Registrando servicio con launchctl (gui/$CONSOLE_UID)..."
  launchctl asuser "$CONSOLE_UID" launchctl bootstrap "gui/$CONSOLE_UID" "$PLIST_PATH"
else
  echo "  No se detectó una sesión gráfica activa; el servicio se activará solo"
  echo "  en el próximo inicio de sesión de cualquier usuario (no requiere reinstalar)."
fi

echo ""
echo "==> COPIME Desktop Agent instalado y activo."
echo "    Logs: $LOG_DIR/copime-agent.log"
echo "    Config: $CONFIG_DIR/config.json"
echo "    Para desinstalar: sudo ./install.sh --uninstall"
