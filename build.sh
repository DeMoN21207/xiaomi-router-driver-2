#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

echo "[info] Native desktop build is deprecated. Building Linux router bundle instead..."
exec bash "$SCRIPT_DIR/package_router.sh"
