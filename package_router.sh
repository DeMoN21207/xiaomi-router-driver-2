#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$SCRIPT_DIR"
FRONTEND_DIR="$ROOT_DIR/frontend"
GO_EXE="$ROOT_DIR/.tools/go/bin/go"
LOCAL_ENV_FILE="$ROOT_DIR/deploy_router.local.sh"
PACKAGE_TEMPLATE_DIR="$ROOT_DIR/packaging/router"

: "${ROUTER_GOOS:=linux}"
: "${ROUTER_GOARCH:=arm64}"
: "${ROUTER_BINARY_NAME:=vpn-manager}"
: "${ROUTER_HTTP_PORT:=18080}"
: "${ROUTER_PACKAGE_DIR:=$ROOT_DIR/build/router}"
: "${ROUTER_DATA_DIR_NAME:=data}"
: "${ROUTER_OPENVPN_BIN:=}"
: "${ROUTER_SINGBOX_BIN:=}"

if [[ -f "$LOCAL_ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$LOCAL_ENV_FILE"
fi

if [[ ! -x "$GO_EXE" ]]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "[error] Go was not found. Install Go or place it in .tools/go." >&2
    exit 1
  fi
  GO_EXE="go"
fi

if ! command -v npm >/dev/null 2>&1; then
  echo "[error] npm was not found in PATH." >&2
  exit 1
fi

if [[ ! -f "$FRONTEND_DIR/package.json" ]]; then
  echo "[error] Frontend directory was not found: $FRONTEND_DIR" >&2
  exit 1
fi

if [[ ! -f "$PACKAGE_TEMPLATE_DIR/README.md" ]]; then
  echo "[error] Router package template was not found: $PACKAGE_TEMPLATE_DIR/README.md" >&2
  exit 1
fi

if [[ ! -f "$PACKAGE_TEMPLATE_DIR/start.sh" ]]; then
  echo "[error] Router package template was not found: $PACKAGE_TEMPLATE_DIR/start.sh" >&2
  exit 1
fi

mkdir -p "$ROUTER_PACKAGE_DIR/$ROUTER_DATA_DIR_NAME" "$ROUTER_PACKAGE_DIR/bin"

echo "[1/4] Building frontend..."
cd "$FRONTEND_DIR"

if [[ ! -d node_modules ]]; then
  if [[ -f package-lock.json ]]; then
    echo "[info] node_modules not found, running npm ci..."
    npm ci
  else
    echo "[info] node_modules not found, running npm install..."
    npm install
  fi
fi

npm run build

echo "[2/4] Building $ROUTER_GOOS/$ROUTER_GOARCH binary..."
cd "$ROOT_DIR"
CGO_ENABLED=0 GOOS="$ROUTER_GOOS" GOARCH="$ROUTER_GOARCH" "$GO_EXE" build -o "$ROUTER_PACKAGE_DIR/$ROUTER_BINARY_NAME" ./cmd/vpn-manager

echo "[3/4] Preparing router bundle..."
cp "$PACKAGE_TEMPLATE_DIR/README.md" "$ROUTER_PACKAGE_DIR/README.md"
cp "$PACKAGE_TEMPLATE_DIR/start.sh" "$ROUTER_PACKAGE_DIR/start.sh"
chmod +x "$ROUTER_PACKAGE_DIR/start.sh"

OPENVPN_BUNDLE_PATH=""
SINGBOX_BUNDLE_PATH=""

if [[ -n "$ROUTER_OPENVPN_BIN" ]]; then
  if [[ ! -f "$ROUTER_OPENVPN_BIN" ]]; then
    echo "[error] ROUTER_OPENVPN_BIN does not exist: $ROUTER_OPENVPN_BIN" >&2
    exit 1
  fi
  cp "$ROUTER_OPENVPN_BIN" "$ROUTER_PACKAGE_DIR/bin/openvpn"
  cp "$ROUTER_OPENVPN_BIN" "$ROUTER_PACKAGE_DIR/openvpn"
  chmod +x "$ROUTER_PACKAGE_DIR/bin/openvpn"
  chmod +x "$ROUTER_PACKAGE_DIR/openvpn"
  OPENVPN_BUNDLE_PATH="openvpn"
elif [[ -f "$ROUTER_PACKAGE_DIR/bin/openvpn" ]]; then
  cp "$ROUTER_PACKAGE_DIR/bin/openvpn" "$ROUTER_PACKAGE_DIR/openvpn"
  chmod +x "$ROUTER_PACKAGE_DIR/openvpn"
  OPENVPN_BUNDLE_PATH="openvpn"
fi

if [[ -n "$ROUTER_SINGBOX_BIN" ]]; then
  if [[ ! -f "$ROUTER_SINGBOX_BIN" ]]; then
    echo "[error] ROUTER_SINGBOX_BIN does not exist: $ROUTER_SINGBOX_BIN" >&2
    exit 1
  fi
  cp "$ROUTER_SINGBOX_BIN" "$ROUTER_PACKAGE_DIR/bin/sing-box"
  cp "$ROUTER_SINGBOX_BIN" "$ROUTER_PACKAGE_DIR/sing-box"
  chmod +x "$ROUTER_PACKAGE_DIR/bin/sing-box"
  chmod +x "$ROUTER_PACKAGE_DIR/sing-box"
  SINGBOX_BUNDLE_PATH="sing-box"
elif [[ -f "$ROUTER_PACKAGE_DIR/bin/sing-box" ]]; then
  cp "$ROUTER_PACKAGE_DIR/bin/sing-box" "$ROUTER_PACKAGE_DIR/sing-box"
  chmod +x "$ROUTER_PACKAGE_DIR/sing-box"
  SINGBOX_BUNDLE_PATH="sing-box"
fi

cat > "$ROUTER_PACKAGE_DIR/bundle-info.txt" <<EOF
binary=$ROUTER_BINARY_NAME
goos=$ROUTER_GOOS
goarch=$ROUTER_GOARCH
default_port=$ROUTER_HTTP_PORT
package_dir=build/router
data_dir=$ROUTER_DATA_DIR_NAME
openvpn_path=$OPENVPN_BUNDLE_PATH
singbox_path=$SINGBOX_BUNDLE_PATH
EOF

echo "[4/4] Router bundle ready: $ROUTER_PACKAGE_DIR"
echo "[done] Copy the whole directory to the router and start it with ./start.sh"
