#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
DEFAULT_BIN="vpn-manager"
DEFAULT_PORT="18080"

if [ -f "$SCRIPT_DIR/bundle-info.txt" ]; then
  BUNDLE_BIN="$(sed -n 's/^binary=//p' "$SCRIPT_DIR/bundle-info.txt" | head -n 1)"
  BUNDLE_PORT="$(sed -n 's/^default_port=//p' "$SCRIPT_DIR/bundle-info.txt" | head -n 1)"

  if [ -n "${BUNDLE_BIN:-}" ]; then
    DEFAULT_BIN="$BUNDLE_BIN"
  fi
  if [ -n "${BUNDLE_PORT:-}" ]; then
    DEFAULT_PORT="$BUNDLE_PORT"
  fi
fi

APP_BIN="${VPN_MANAGER_BINARY:-$DEFAULT_BIN}"
APP_PATH="$SCRIPT_DIR/$APP_BIN"
PORT="${VPN_MANAGER_PORT:-$DEFAULT_PORT}"

if [ ! -x "$APP_PATH" ]; then
  echo "[error] executable not found or not executable: $APP_PATH" >&2
  exit 1
fi

export VPN_MANAGER_ROOT="${VPN_MANAGER_ROOT:-$SCRIPT_DIR}"
export VPN_MANAGER_PORT="$PORT"
export PATH="$SCRIPT_DIR:$SCRIPT_DIR/bin:$SCRIPT_DIR/.vpn-manager/bin:$PATH"

exec "$APP_PATH"
