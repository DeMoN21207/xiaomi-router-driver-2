#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$SCRIPT_DIR"
LOCAL_ENV_FILE="$ROOT_DIR/deploy_router.local.sh"
LOCAL_ENV_CMD_FILE="$ROOT_DIR/deploy_router.local.cmd"
LOCAL_ENV_EXAMPLE_FILE="$ROOT_DIR/deploy_router.local.example.sh"

: "${ROUTER_HOST:=192.168.31.1}"
: "${ROUTER_PORT:=22}"
: "${ROUTER_USER:=root}"
: "${ROUTER_REMOTE_DIR:=/mnt/usb-4d3e56cb/vpn-manager}"
: "${ROUTER_SERVICE:=vpn-manager}"
: "${ROUTER_HTTP_PORT:=18080}"
: "${ROUTER_GOOS:=linux}"
: "${ROUTER_GOARCH:=arm64}"
: "${ROUTER_BINARY_NAME:=vpn-manager}"
: "${ROUTER_PACKAGE_DIR:=$ROOT_DIR/build/router}"
: "${ROUTER_FETCH_RUNTIME_FROM_DEVICE:=1}"
: "${ROUTER_HEALTH_RETRIES:=20}"
: "${ROUTER_HEALTH_DELAY:=1}"
: "${ROUTER_SCP_LEGACY:=1}"
: "${ROUTER_SSH_STRICT:=accept-new}"

TEMP_KNOWN_HOSTS=""

cleanup() {
  if [[ -n "$TEMP_KNOWN_HOSTS" ]]; then
    rm -f "$TEMP_KNOWN_HOSTS"
  fi
}
trap cleanup EXIT

load_cmd_env() {
  local file="$1"
  local line key value

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    if [[ "$line" =~ ^[[:space:]]*set[[:space:]]+\"([A-Za-z_][A-Za-z0-9_]*)=(.*)\"[[:space:]]*$ ]]; then
      key="${BASH_REMATCH[1]}"
      value="${BASH_REMATCH[2]}"
      printf -v "$key" '%s' "$value"
      export "$key"
    fi
  done < "$file"
}

if [[ -f "$LOCAL_ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$LOCAL_ENV_FILE"
elif [[ -f "$LOCAL_ENV_CMD_FILE" ]]; then
  load_cmd_env "$LOCAL_ENV_CMD_FILE"
fi

require_command() {
  local name="$1"
  if ! command -v "$name" >/dev/null 2>&1; then
    echo "[error] $name was not found in PATH." >&2
    exit 1
  fi
}

sh_quote() {
  local value="$1"
  printf "'"
  printf "%s" "$value" | sed "s/'/'\\\\''/g"
  printf "'"
}

ssh_password_wrap() {
  ROUTER_PASSWORD="$ROUTER_PASSWORD" expect -f - "$@" <<'EXPECT_EOF'
set timeout -1
set password $env(ROUTER_PASSWORD)
spawn {*}$argv
expect {
  -re "(?i)are you sure you want to continue connecting" {
    send -- "yes\r"
    exp_continue
  }
  -re "(?i)(password|passphrase).*:" {
    send -- "$password\r"
    exp_continue
  }
  eof {
    catch wait result
    exit [lindex $result 3]
  }
}
EXPECT_EOF
}

prepare_host_key_options() {
  SSH_HOST_OPTS=(-o HostKeyAlgorithms=+ssh-rsa -o PubkeyAcceptedAlgorithms=+ssh-rsa)

  if [[ -n "${ROUTER_HOSTKEY:-}" ]]; then
    local expected_fingerprint keyscan_output fingerprints
    expected_fingerprint="$(awk '{print $3}' <<<"$ROUTER_HOSTKEY")"

    if [[ -z "$expected_fingerprint" || "$expected_fingerprint" != SHA256:* ]]; then
      echo "[error] ROUTER_HOSTKEY must look like: ssh-rsa 2048 SHA256:..." >&2
      exit 1
    fi

    TEMP_KNOWN_HOSTS="$(mktemp)"
    if ! keyscan_output="$(ssh-keyscan -p "$ROUTER_PORT" "$ROUTER_HOST" 2>/dev/null)"; then
      echo "[error] Failed to read SSH host key from $ROUTER_HOST:$ROUTER_PORT." >&2
      exit 1
    fi

    if [[ -z "$keyscan_output" ]]; then
      echo "[error] Router did not return an SSH host key." >&2
      exit 1
    fi

    printf "%s\n" "$keyscan_output" > "$TEMP_KNOWN_HOSTS"
    fingerprints="$(ssh-keygen -lf "$TEMP_KNOWN_HOSTS" -E sha256 2>/dev/null || ssh-keygen -lf "$TEMP_KNOWN_HOSTS")"
    if ! grep -F "$expected_fingerprint" <<<"$fingerprints" >/dev/null 2>&1; then
      echo "[error] Router SSH host key fingerprint does not match ROUTER_HOSTKEY." >&2
      echo "[hint] Expected: $expected_fingerprint" >&2
      echo "[hint] Seen:" >&2
      printf "%s\n" "$fingerprints" >&2
      exit 1
    fi

    SSH_HOST_OPTS+=(-o "UserKnownHostsFile=$TEMP_KNOWN_HOSTS" -o StrictHostKeyChecking=yes)
  else
    SSH_HOST_OPTS+=(-o "StrictHostKeyChecking=$ROUTER_SSH_STRICT")
  fi
}

ssh_run() {
  local base=(ssh -p "$ROUTER_PORT" "${SSH_HOST_OPTS[@]}")

  if [[ -n "${ROUTER_PASSWORD:-}" ]]; then
    if command -v sshpass >/dev/null 2>&1; then
      SSHPASS="$ROUTER_PASSWORD" sshpass -e "${base[@]}" "$@"
    elif command -v expect >/dev/null 2>&1; then
      ssh_password_wrap "${base[@]}" "$@"
    else
      echo "[error] ROUTER_PASSWORD is set, but neither sshpass nor expect is available." >&2
      echo "[hint] Install sshpass, install expect, or configure SSH key auth and unset ROUTER_PASSWORD." >&2
      exit 1
    fi
  else
    "${base[@]}" -o BatchMode=yes "$@"
  fi
}

scp_to() {
  local local_path="$1"
  local remote_path="$2"
  local scp_opts=()
  local base

  if [[ "$ROUTER_SCP_LEGACY" == "1" ]]; then
    scp_opts=(-O)
  fi
  base=(scp "${scp_opts[@]}" -P "$ROUTER_PORT" "${SSH_HOST_OPTS[@]}")

  if [[ -n "${ROUTER_PASSWORD:-}" ]]; then
    if command -v sshpass >/dev/null 2>&1; then
      SSHPASS="$ROUTER_PASSWORD" sshpass -e "${base[@]}" "$local_path" "$ROUTER_USER@$ROUTER_HOST:$remote_path"
    elif command -v expect >/dev/null 2>&1; then
      ssh_password_wrap "${base[@]}" "$local_path" "$ROUTER_USER@$ROUTER_HOST:$remote_path"
    else
      echo "[error] ROUTER_PASSWORD is set, but neither sshpass nor expect is available." >&2
      exit 1
    fi
  else
    "${base[@]}" -o BatchMode=yes "$local_path" "$ROUTER_USER@$ROUTER_HOST:$remote_path"
  fi
}

scp_from() {
  local remote_path="$1"
  local local_path="$2"
  local scp_opts=()
  local base

  if [[ "$ROUTER_SCP_LEGACY" == "1" ]]; then
    scp_opts=(-O)
  fi
  base=(scp "${scp_opts[@]}" -P "$ROUTER_PORT" "${SSH_HOST_OPTS[@]}")

  if [[ -n "${ROUTER_PASSWORD:-}" ]]; then
    if command -v sshpass >/dev/null 2>&1; then
      SSHPASS="$ROUTER_PASSWORD" sshpass -e "${base[@]}" "$ROUTER_USER@$ROUTER_HOST:$remote_path" "$local_path"
    elif command -v expect >/dev/null 2>&1; then
      ssh_password_wrap "${base[@]}" "$ROUTER_USER@$ROUTER_HOST:$remote_path" "$local_path"
    else
      echo "[error] ROUTER_PASSWORD is set, but neither sshpass nor expect is available." >&2
      exit 1
    fi
  else
    "${base[@]}" -o BatchMode=yes "$ROUTER_USER@$ROUTER_HOST:$remote_path" "$local_path"
  fi
}

remote_script() {
  local local_script remote_path
  local_script="$(mktemp)"
  remote_path="/tmp/vpn-manager-deploy-$(date +%s)-$$.sh"

  cat > "$local_script"
  scp_to "$local_script" "$remote_path"
  rm -f "$local_script"

  ssh_run "$ROUTER_USER@$ROUTER_HOST" sh "$remote_path"
  ssh_run "$ROUTER_USER@$ROUTER_HOST" rm -f "$remote_path" >/dev/null 2>&1 || true
}

json_extract_automation() {
  if command -v jq >/dev/null 2>&1; then
    jq -c '.automation'
  else
    node -e 'let s = ""; process.stdin.on("data", d => s += d); process.stdin.on("end", () => process.stdout.write(JSON.stringify(JSON.parse(s).automation)));'
  fi
}

json_check_status() {
  if command -v jq >/dev/null 2>&1; then
    jq -e '(.binaries.openvpn == true) and (.binaries.singbox == true) and ((.lastError // "") == "")' >/dev/null
  else
    node -e 'let s = ""; process.stdin.on("data", d => s += d); process.stdin.on("end", () => { const x = JSON.parse(s); if (!x.binaries?.openvpn || !x.binaries?.singbox) throw new Error("runtime binaries are not ready after deploy"); if (x.lastError) throw new Error("deploy health error: " + x.lastError); });'
  fi
}

if [[ -z "${ROUTER_REMOTE_DIR:-}" || "$ROUTER_REMOTE_DIR" == "/" ]]; then
  echo "[error] ROUTER_REMOTE_DIR must be set and must not be /." >&2
  exit 1
fi

if [[ -n "${ROUTER_PASSWORD:-}" ]]; then
  if ! command -v sshpass >/dev/null 2>&1 && ! command -v expect >/dev/null 2>&1; then
    echo "[error] ROUTER_PASSWORD is set, but neither sshpass nor expect is available." >&2
    exit 1
  fi
elif [[ ! -f "$LOCAL_ENV_FILE" && ! -f "$LOCAL_ENV_CMD_FILE" ]]; then
  echo "[hint] No local deploy config found. Create $LOCAL_ENV_FILE from $LOCAL_ENV_EXAMPLE_FILE for password auth." >&2
fi

require_command ssh
require_command scp
require_command ssh-keyscan
require_command ssh-keygen
require_command curl
if ! command -v jq >/dev/null 2>&1 && ! command -v node >/dev/null 2>&1; then
  echo "[error] jq or node is required for deploy API JSON handling." >&2
  exit 1
fi
prepare_host_key_options

PACKAGE_OPENVPN="$ROUTER_PACKAGE_DIR/bin/openvpn"
PACKAGE_SINGBOX="$ROUTER_PACKAGE_DIR/bin/sing-box"

if [[ "$ROUTER_FETCH_RUNTIME_FROM_DEVICE" == "1" ]]; then
  mkdir -p "$ROUTER_PACKAGE_DIR/bin"

  if [[ -z "${ROUTER_OPENVPN_BIN:-}" && ! -f "$PACKAGE_OPENVPN" ]]; then
    echo "[prep] Fetching openvpn from the router..."
    if ! scp_from "$ROUTER_REMOTE_DIR/openvpn" "$PACKAGE_OPENVPN"; then
      scp_from "$ROUTER_REMOTE_DIR/bin/openvpn" "$PACKAGE_OPENVPN"
    fi
  fi

  if [[ -z "${ROUTER_SINGBOX_BIN:-}" && ! -f "$PACKAGE_SINGBOX" ]]; then
    echo "[prep] Fetching sing-box from the router..."
    if ! scp_from "$ROUTER_REMOTE_DIR/sing-box" "$PACKAGE_SINGBOX"; then
      scp_from "$ROUTER_REMOTE_DIR/bin/sing-box" "$PACKAGE_SINGBOX"
    fi
  fi
fi

echo "[1/7] Building Linux router bundle..."
bash "$ROOT_DIR/package_router.sh"

if [[ ! -x "$ROUTER_PACKAGE_DIR/$ROUTER_BINARY_NAME" ]]; then
  echo "[error] Router bundle binary was not found or is not executable: $ROUTER_PACKAGE_DIR/$ROUTER_BINARY_NAME" >&2
  exit 1
fi

remote_dir_q="$(sh_quote "$ROUTER_REMOTE_DIR")"
remote_service_q="$(sh_quote "$ROUTER_SERVICE")"

echo "[2/7] Cleaning remote bundle directory and preserving data..."
remote_script <<REMOTE_EOF
set -e
remote_dir=$remote_dir_q
service=$remote_service_q
tmp="\${remote_dir}.data-preserve"
rm -rf "\$tmp"
pkill -x "\$service" 2>/dev/null || true
if [ -x /sbin/start-stop-daemon ]; then
  /sbin/start-stop-daemon -K -q -p "/tmp/\$service.pid" 2>/dev/null || true
fi
if [ -d "\$remote_dir/data" ]; then
  mv "\$remote_dir/data" "\$tmp"
fi
rm -rf "\$remote_dir"
mkdir -p "\$remote_dir"
if [ -d "\$tmp" ]; then
  mv "\$tmp" "\$remote_dir/data"
else
  mkdir -p "\$remote_dir/data"
fi
mkdir -p "\$remote_dir/bin"
REMOTE_EOF

echo "[3/7] Uploading main bundle files..."
scp_to "$ROUTER_PACKAGE_DIR/$ROUTER_BINARY_NAME" "$ROUTER_REMOTE_DIR/$ROUTER_BINARY_NAME"

for name in start.sh README.md bundle-info.txt; do
  if [[ -f "$ROUTER_PACKAGE_DIR/$name" ]]; then
    scp_to "$ROUTER_PACKAGE_DIR/$name" "$ROUTER_REMOTE_DIR/$name"
  fi
done

echo "[4/7] Uploading bundled runtime binaries..."
for name in openvpn sing-box; do
  if [[ -f "$ROUTER_PACKAGE_DIR/bin/$name" ]]; then
    scp_to "$ROUTER_PACKAGE_DIR/bin/$name" "$ROUTER_REMOTE_DIR/bin/$name"
  fi
  if [[ -f "$ROUTER_PACKAGE_DIR/$name" ]]; then
    scp_to "$ROUTER_PACKAGE_DIR/$name" "$ROUTER_REMOTE_DIR/$name"
  fi
done

binary_q="$(sh_quote "$ROUTER_BINARY_NAME")"
http_port_q="$(sh_quote "$ROUTER_HTTP_PORT")"
health_retries_q="$(sh_quote "$ROUTER_HEALTH_RETRIES")"
health_delay_q="$(sh_quote "$ROUTER_HEALTH_DELAY")"

echo "[5/7] Starting router service and waiting for healthcheck..."
remote_script <<REMOTE_EOF
set -e
remote_dir=$remote_dir_q
service=$remote_service_q
binary=$binary_q
http_port=$http_port_q
health_retries=$health_retries_q
health_delay=$health_delay_q

cd "\$remote_dir"
chmod +x "\$binary"
[ -f start.sh ] && chmod +x start.sh
[ -f openvpn ] && chmod +x openvpn
[ -f sing-box ] && chmod +x sing-box
[ -f bin/openvpn ] && chmod +x bin/openvpn
[ -f bin/sing-box ] && chmod +x bin/sing-box
pkill -x "\$service" 2>/dev/null || true
if [ -x /sbin/start-stop-daemon ]; then
  /sbin/start-stop-daemon -K -q -p "/tmp/\$service.pid" 2>/dev/null || true
fi
rm -f "/tmp/\$service.pid"
export VPN_MANAGER_ROOT="\$remote_dir"
export VPN_MANAGER_PORT="\$http_port"
export PATH="\$remote_dir:\$remote_dir/bin:\$remote_dir/.vpn-manager/bin:/usr/sbin:/usr/bin:/sbin:/bin"

if [ -x /sbin/start-stop-daemon ]; then
  /sbin/start-stop-daemon -S -q -b -m -p "/tmp/\$service.pid" -x "./\$binary"
else
  "./\$binary" >"/tmp/\$service.log" 2>&1 </dev/null &
fi

attempt=0
while [ "\$attempt" -lt "\$health_retries" ]; do
  if command -v wget >/dev/null 2>&1; then
    if wget -qO "/tmp/\$service-health.json" "http://127.0.0.1:\$http_port/api/status" 2>/dev/null; then
      head -c 400 "/tmp/\$service-health.json"
      exit 0
    fi
  elif command -v curl >/dev/null 2>&1; then
    if curl -fsS "http://127.0.0.1:\$http_port/api/status" -o "/tmp/\$service-health.json" 2>/dev/null; then
      head -c 400 "/tmp/\$service-health.json"
      exit 0
    fi
  fi
  attempt=\$((attempt + 1))
  sleep "\$health_delay"
done

echo "healthcheck failed after \$health_retries attempts" >&2
exit 1
REMOTE_EOF

echo
echo "[6/7] Reinstalling automation bootstrap and reconciling routes..."
api_base="http://$ROUTER_HOST:$ROUTER_HTTP_PORT"
automation_body="$(curl -fsS --max-time 20 "$api_base/api/config/automation" | json_extract_automation)"
curl -fsS --max-time 20 \
  -X PUT \
  -H "Content-Type: application/json" \
  --data "$automation_body" \
  "$api_base/api/config/automation" >/dev/null

curl -fsS --max-time 60 -X POST "$api_base/api/rules/apply" >/dev/null || sleep 2
curl -fsS --max-time 30 "$api_base/api/status" | json_check_status

echo
echo "[7/7] Bundle deployed: $ROUTER_PACKAGE_DIR"
echo "[done] UI: $api_base"
