#!/usr/bin/env bash
# install.sh — Instala o desinstala el COPIME Desktop Agent (CDA) en macOS.
#
# Uso:
#   sudo ./install.sh --server URL --enroll-code CODE
#   sudo ./install.sh --uninstall
#
# El script requiere permisos de root para:
#   - Copiar el binario a /usr/local/bin/
#   - Crear /etc/copime-agent/ (modo 700)
#   - Instalar el plist en /Library/LaunchDaemons/
#   - Registrar el servicio con launchctl

set -euo pipefail

BINARY_NAME="cda"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/copime-agent"
PLIST_PATH="/Library/LaunchDaemons/com.conasinf.copime-agent.plist"
PLIST_LABEL="com.conasinf.copime-agent"

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

  # Detener y retirar el servicio de launchd.
  if launchctl list "$PLIST_LABEL" &>/dev/null; then
    echo "  Retirando servicio de launchd..."
    launchctl bootout system "$PLIST_PATH" 2>/dev/null || true
  fi

  # Eliminar archivos del agente.
  [ -f "$PLIST_PATH" ] && rm -f "$PLIST_PATH" && echo "  Eliminado: $PLIST_PATH"
  [ -f "$INSTALL_DIR/$BINARY_NAME" ] && rm -f "$INSTALL_DIR/$BINARY_NAME" && echo "  Eliminado: $INSTALL_DIR/$BINARY_NAME"
  [ -d "$CONFIG_DIR" ] && rm -rf "$CONFIG_DIR" && echo "  Eliminado: $CONFIG_DIR"

  echo "==> Desinstalación completa."
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

# 2. Crear directorio de configuración con permisos restringidos.
echo "  Creando directorio de configuración $CONFIG_DIR (modo 700)..."
install -d -m 700 "$CONFIG_DIR"

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

# 5. Registrar el servicio con launchd.
echo "  Registrando servicio con launchctl..."
launchctl bootstrap system "$PLIST_PATH"

echo ""
echo "==> COPIME Desktop Agent instalado y activo."
echo "    Logs: /var/log/copime-agent.log"
echo "    Config: $CONFIG_DIR/config.json"
echo "    Para desinstalar: sudo ./install.sh --uninstall"
