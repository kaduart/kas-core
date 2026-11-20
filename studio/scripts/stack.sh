#!/usr/bin/env bash
# Stack local e descartável para o Studio: Postgres (Docker, ou PG_ADMIN_URL),
# KAS real com -process-store postgres (papel de runtime sem DDL) e um CRM
# simulado por empresa, consumindo pelo pull real do Outbox. Empresas, atores e
# contratos fictícios; tudo em loopback.
#
#   studio/scripts/stack.sh start   # sobe e publica a versão 1 dos fluxos de demonstração
#   source studio/.stack/env        # tokens só para o servidor do Studio (nunca vão ao navegador)
#   (cd studio && npm run dev)      # http://127.0.0.1:5174
#   studio/scripts/stack.sh stop
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
STUDIO=$(cd "$HERE/.." && pwd)
ROOT=$(cd "$STUDIO/.." && pwd)
STACK="$STUDIO/.stack"
PORT=${KAS_PORT:-18288}
DBPORT=${DBPORT:-55433}
DBNAME=kas_studio_demo
RT_ROLE=kas_studio_rt
PG=kas-studio-pg
KAS="http://127.0.0.1:$PORT"

stop() {
  if [ -f "$STACK/pids" ]; then while read -r p; do kill "$p" 2>/dev/null || true; done < "$STACK/pids"; rm -f "$STACK/pids"; fi
  [ -f "$STACK/docker" ] && docker rm -f "$PG" >/dev/null 2>&1 && rm -f "$STACK/docker"
  return 0
}

start() {
  stop
  mkdir -p "$STACK"
  if [ -z "${PG_ADMIN_URL:-}" ]; then
    docker rm -f "$PG" >/dev/null 2>&1 || true
    docker run -d --name "$PG" -p 127.0.0.1:$DBPORT:5432 -e POSTGRES_USER=kas -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16 >/dev/null
    touch "$STACK/docker"
    for i in $(seq 1 60); do docker exec "$PG" pg_isready -U kas >/dev/null 2>&1 && break; sleep 1; done
    sleep 2
    PG_ADMIN_URL="postgres://kas@127.0.0.1:$DBPORT/postgres?sslmode=disable"
  fi
  local base=${PG_ADMIN_URL%%\?*}; base=${base%/*}
  local migrate="$base/$DBNAME?sslmode=disable"
  local runtime; runtime="$(echo "$base" | sed -E "s#//[^@]*@#//$RT_ROLE@#")/$DBNAME?sslmode=disable"
  psql "$PG_ADMIN_URL" -q -v ON_ERROR_STOP=1 <<SQL
DROP DATABASE IF EXISTS $DBNAME WITH (FORCE);
CREATE DATABASE $DBNAME;
DO \$\$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$RT_ROLE') THEN CREATE ROLE $RT_ROLE LOGIN; END IF; END\$\$;
SQL
  (cd "$ROOT" && go build -o "$STACK/kas-core" ./cmd/kas-core && go build -o "$STACK/kas-crm-sim" ./cmd/kas-crm-sim)
  KAS_AUDIT_MIGRATION_DATABASE_URL="$migrate" KAS_EVENTS_MIGRATION_DATABASE_URL="$migrate" \
    "$STACK/kas-core" -audit-store postgres -audit-migrate-only -events-store postgres -events-migrate-only \
    -process-store postgres -process-migrate-only > "$STACK/migrate.log"
  psql "$migrate" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants.sql"
  psql "$migrate" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants-process.sql"

  local ta tb; ta=$(openssl rand -hex 32); tb=$(openssl rand -hex 32)
  cat > "$STACK/env" <<ENV
export KAS_URL=$KAS
export DEMO_CRM_A_TOKEN=$ta
export DEMO_CRM_B_TOKEN=$tb
ENV
  chmod 600 "$STACK/env"
  # shellcheck disable=SC1091
  source "$STACK/env"
  local types='"process.command.requested","process.command.status_query","process.wait.opened"'
  local caps='"events:consume","process:define","process:run","process:task","process:command","process:event"'
  cat > "$STACK/config.json" <<JSON
{"contract_version":"v1","config_version":"studio-demo",
 "tenant_registry":[{"id":"demo-empresa-a","status":"active"},{"id":"demo-empresa-b","status":"active"}],
 "key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{},
 "event_types":[$types],
 "event_consumers":{
  "crm-simulado-a":{"service":"demo-crm-a","event_types":[$types],"tenants":["demo-empresa-a"]},
  "crm-simulado-b":{"service":"demo-crm-b","event_types":[$types],"tenants":["demo-empresa-b"]}},
 "callers":{
  "demo-crm-a":{"token_env":"DEMO_CRM_A_TOKEN","tenants":["demo-empresa-a"],"capabilities":[$caps]},
  "demo-crm-b":{"token_env":"DEMO_CRM_B_TOKEN","tenants":["demo-empresa-b"],"capabilities":[$caps]}}}
JSON
  KAS_EVENTS_DATABASE_URL="$runtime" KAS_AUDIT_DATABASE_URL="$runtime" nohup "$STACK/kas-core" -config "$STACK/config.json" \
    -audit-store postgres -audit-auto-migrate=false -events-store postgres -events-auto-migrate=false \
    -process-store postgres -process-auto-migrate=false -addr 127.0.0.1:$PORT > "$STACK/kas.log" 2>&1 &
  echo $! >> "$STACK/pids"
  for i in $(seq 1 50); do curl -fsS "$KAS/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
  curl -fsS "$KAS/readyz" >/dev/null || { tail -20 "$STACK/kas.log"; exit 1; }
  for l in a b; do
    local up; up=$(echo $l | tr a-z A-Z)
    nohup "$STACK/kas-crm-sim" -kas "$KAS" -service demo-crm-$l -token-env DEMO_CRM_${up}_TOKEN -consumer crm-simulado-$l \
      -state "$STACK/sim-$l.json" -interval 300ms -lease-seconds 5 > "$STACK/sim-$l.log" 2>&1 &
    echo $! >> "$STACK/pids"
  done
  node "$HERE/seed.mjs"
  echo "Stack pronta: KAS em $KAS. Rode: source $STACK/env"
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *) echo "uso: $0 start|stop"; exit 2 ;;
esac
