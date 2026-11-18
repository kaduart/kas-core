#!/bin/bash
# Development only: creates a self-signed TLS certificate for the local stack.
# KAS requires TLS on non-loopback listeners and for non-loopback PostgreSQL
# hosts, so the compose stack uses this certificate for both.
#
# The private key is made readable by every local user (0644) because the
# containers run as different unprivileged users and read it through a bind
# mount. Never use this key or this script outside a development machine.
set -euo pipefail
dir="$(cd "$(dirname "$0")/.." && pwd)/deploy/dev/certs"
mkdir -p "$dir"
if [[ -f "$dir/server.crt" && -f "$dir/server.key" && "${1:-}" != "--force" ]]; then
  echo "certificate already exists in $dir (use --force to recreate)"
  exit 0
fi
openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
  -subj "/CN=kas-core-dev" \
  -addext "subjectAltName=DNS:localhost,DNS:kas,DNS:db,IP:127.0.0.1" \
  -keyout "$dir/server.key" -out "$dir/server.crt" 2>/dev/null
chmod 644 "$dir/server.key" "$dir/server.crt"
echo "development certificate written to $dir (valid 30 days, self-signed)"
