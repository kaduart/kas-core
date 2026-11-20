#!/usr/bin/env bash
# Piloto da segunda empresa fictícia (escola-demo), isolado e descartável.
#
# Sobe o executável REAL da KAS com -attribution-store postgres (papel de
# runtime sem DDL, grants de deploy/grants.sql + deploy/grants-attribution.sql)
# e importa os CSVs com o executável REAL kas-attribution-import. Empresas,
# e-mails e telefones fictícios. Nada fala com Render, Atlas, CRM real ou
# plataformas de anúncio; tudo em loopback.
#
#   deploy/piloto-escola/run.sh                    # Postgres 16 descartável em Docker
#   PG_ADMIN_URL=postgres://kas@127.0.0.1:5433/postgres?sslmode=disable deploy/piloto-escola/run.sh
#   KEEP=1 deploy/piloto-escola/run.sh             # mantém logs, banco e o relatório HTML
#
# Os 7 passos da demonstração (cada mudança de contrato é uma nova
# configuração e um reinício, como faria o administrador):
#  1. cria a empresa (registrada, nenhum módulo contratado);
#  2. ativa attribution + reports;
#  3. importa os 4 arquivos: o relatório fecha;
#  4. reimporta: mesmos totais, nada criado;
#  5. pausa: 403 module_paused, dados mantidos; reativa: mesmos números;
#  6. isolamento com fono-demo, nos dois sentidos;
#  7. nenhum código próprio da empresa nos módulos.
# Só termina com "TUDO OK" se todos passarem.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$(mktemp -d)}; mkdir -p "$WORK"
PORT=${PORT:-18288}
DBPORT=${DBPORT:-55433}
DBNAME=kas_piloto_escola
RT_ROLE=kas_piloto_escola_rt
PG=kas-piloto-escola-pg
KAS="http://127.0.0.1:$PORT"
KAS_PID=""
FAILS=0
DATA="$ROOT/testdata/attribution"

say() { printf '\n== %s\n' "$*"; }
ok() { printf 'OK: %s\n' "$*"; }
bad() { printf 'FALHOU: %s\n' "$*"; FAILS=$((FAILS + 1)); }
cleanup() {
  local rc=$?
  set +e
  [ -n "$KAS_PID" ] && kill "$KAS_PID" 2>/dev/null && wait "$KAS_PID" 2>/dev/null
  if [ "${KEEP:-0}" = 1 ]; then echo "logs e relatório mantidos em $WORK"; else
    [ -z "${PG_ADMIN_URL_GIVEN:-}" ] && docker rm -f "$PG" >/dev/null 2>&1
  fi
  exit "$rc"
}
trap cleanup EXIT

if [ -n "${PG_ADMIN_URL:-}" ]; then
  PG_ADMIN_URL_GIVEN=1
else
  say "Postgres 16 descartável (Docker, só loopback)"
  docker rm -f "$PG" >/dev/null 2>&1 || true
  docker run -d --name "$PG" -p 127.0.0.1:$DBPORT:5432 -e POSTGRES_USER=kas -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16 >/dev/null
  for i in $(seq 1 60); do docker exec "$PG" pg_isready -U kas >/dev/null 2>&1 && break; sleep 1; done
  sleep 2
  PG_ADMIN_URL="postgres://kas@127.0.0.1:$DBPORT/postgres?sslmode=disable"
fi
BASE_URL=${PG_ADMIN_URL%%\?*}; BASE_URL=${BASE_URL%/*}
MIGRATE_URL="$BASE_URL/$DBNAME?sslmode=disable"
RUNTIME_URL="$(echo "$BASE_URL" | sed -E "s#//[^@]*@#//$RT_ROLE@#")/$DBNAME?sslmode=disable"

say "Banco $DBNAME novo e papel de runtime sem DDL"
psql "$PG_ADMIN_URL" -q -v ON_ERROR_STOP=1 <<SQL
DROP DATABASE IF EXISTS $DBNAME WITH (FORCE);
CREATE DATABASE $DBNAME;
DO \$\$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$RT_ROLE') THEN CREATE ROLE $RT_ROLE LOGIN; END IF; END\$\$;
SQL

say "Compilando os executáveis"
(cd "$ROOT" && go build -o "$WORK/kas-core" ./cmd/kas-core && go build -o "$WORK/kas-attribution-import" ./cmd/kas-attribution-import)

say "Migrações com o papel de migração e grants"
KAS_AUDIT_MIGRATION_DATABASE_URL="$MIGRATE_URL" KAS_EVENTS_MIGRATION_DATABASE_URL="$MIGRATE_URL" \
  "$WORK/kas-core" -audit-store postgres -audit-migrate-only -events-store postgres -events-migrate-only \
  -attribution-store postgres -attribution-migrate-only > "$WORK/migrate.log"
psql "$MIGRATE_URL" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants.sql"
psql "$MIGRATE_URL" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants-attribution.sql"
ok "migrações e grants aplicados"

# Tokens e chave de impressão digital só deste teste, gerados agora.
export PILOTO_FONO_TOKEN=$(openssl rand -hex 32) PILOTO_ESCOLA_TOKEN=$(openssl rand -hex 32)
export KAS_ATTRIBUTION_FINGERPRINT_KEY=$(openssl rand -hex 32)
ALL_CAPS='"attribution:import","attribution:read","reports:read"'

# config ESTADO: sem-escola | criada | ativa | pausada
config() {
  local tenants='{"id":"fono-demo","status":"active"}' values='"fono-demo":{"branding.name":"Clínica Fictícia"}'
  local modules='"fono-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}}'
  local callers="\"fono-crm\":{\"token_env\":\"PILOTO_FONO_TOKEN\",\"tenants\":[\"fono-demo\"],\"capabilities\":[$ALL_CAPS]}"
  if [ "$1" != sem-escola ]; then
    tenants="$tenants,{\"id\":\"escola-demo\",\"status\":\"active\"}"
    values="$values,\"escola-demo\":{\"branding.name\":\"Escola Fictícia Aurora\",\"branding.color\":\"#3355cc\"}"
    local caps="" st=""
    case "$1" in ativa) st=active ;; pausada) st=paused ;; esac
    if [ -n "$st" ]; then
      modules="$modules,\"escola-demo\":{\"attribution\":{\"status\":\"$st\"},\"reports\":{\"status\":\"$st\"}}"
      caps=$ALL_CAPS
    fi
    callers="$callers,\"escola-sis\":{\"token_env\":\"PILOTO_ESCOLA_TOKEN\",\"tenants\":[\"escola-demo\"],\"capabilities\":[$caps]}"
  fi
  cat > "$WORK/config.json" <<JSON
{"contract_version":"v1","config_version":"piloto-escola-$1",
 "tenant_registry":[$tenants],
 "key_schema":{"branding.name":{"type":"string"},"branding.color":{"type":"string"}},
 "tenants":{$values},
 "modules":{$modules},
 "callers":{$callers}}
JSON
}

start_kas() {
  [ -n "$KAS_PID" ] && kill "$KAS_PID" 2>/dev/null && wait "$KAS_PID" 2>/dev/null || true
  KAS_EVENTS_DATABASE_URL="$RUNTIME_URL" KAS_AUDIT_DATABASE_URL="$RUNTIME_URL" "$WORK/kas-core" -config "$WORK/config.json" \
    -audit-store postgres -audit-auto-migrate=false -events-store postgres -events-auto-migrate=false \
    -attribution-store postgres -attribution-auto-migrate=false -addr 127.0.0.1:$PORT >> "$WORK/kas.log" 2>&1 &
  KAS_PID=$!
  for i in $(seq 1 50); do curl -fsS "$KAS/readyz" >/dev/null 2>&1 && return 0; sleep 0.2; done
  echo "KAS não ficou pronta"; tail -20 "$WORK/kas.log"; exit 1
}

# api METHOD PATH SERVICE TOKEN -> corpo em $WORK/out.json, devolve o status HTTP
api() {
  curl -sS -o "$WORK/out.json" -w '%{http_code}' -X "$1" -H "X-KAS-Service: $3" -H "Authorization: Bearer $4" "$KAS$2"
}
field() { jq -r "$1" "$WORK/out.json"; }

# load SERVICE TOKEN_ENV DIR BATCH -> importa os 4 arquivos; resumo em $WORK/sum-<kind>.json
load() {
  for kind in customers contacts receipts refunds; do
    "$WORK/kas-attribution-import" -kas "$KAS" -service "$1" -token-env "$2" -kind "$kind" \
      -file "$DATA/$3/$kind.csv" -batch-id "$4-$kind" -actor user.fin-07 > "$WORK/sum-$kind.json"
  done
}

# revenue SERVICE TOKEN -> "total comprovada provavel sem_vinculo fecha"
revenue() {
  local code; code=$(api GET /internal/v1/attribution/revenue "$1" "$2")
  [ "$code" = 200 ] || { echo "relatório $code $(cat "$WORK/out.json")"; return; }
  field '"\(.total_liquido_centavos) \(.por_nivel_centavos.comprovada // 0) \(.por_nivel_centavos.provavel // 0) \(.por_nivel_centavos.sem_vinculo // 0) \(.invariante.fecha)"'
}

say "Antes: só fono-demo existe e tem seus dados"
config sem-escola; start_kas
load fono-crm PILOTO_FONO_TOKEN amostra-fono fono
FONO=$(revenue fono-crm "$PILOTO_FONO_TOKEN")
[ "$FONO" = "129000 48000 57000 24000 true" ] && ok "fono-demo fecha R\$ 1.290,00 ($FONO)" || bad "fono-demo: $FONO"

say "1. Empresa criada, nenhum módulo contratado"
config criada; start_kas
api GET /internal/v1/modules escola-sis "$PILOTO_ESCOLA_TOKEN" >/dev/null
[ "$(field '[.modules[] | select(.key=="attribution" or .key=="reports") | .contract] | join(",")')" = "absent,absent" ] \
  && ok "attribution e reports aparecem como não contratados" || bad "visão: $(cat "$WORK/out.json")"

say "2. Attribution + Reports ativados"
config ativa; start_kas
api GET /internal/v1/modules escola-sis "$PILOTO_ESCOLA_TOKEN" >/dev/null
[ "$(field '.modules[] | select(.key=="attribution") | "\(.contract) \(.integration.missing | join(","))"')" = "active contacts,receipts" ] \
  && ok "ativo, integração aguardando contatos e recebimentos" || bad "visão: $(cat "$WORK/out.json")"

say "3. Importação dos 4 arquivos da escola"
load escola-sis PILOTO_ESCOLA_TOKEN escola-demo carga-1
ESCOLA=$(revenue escola-sis "$PILOTO_ESCOLA_TOKEN")
[ "$ESCOLA" = "647000 267000 285000 95000 true" ] && ok "relatório fecha R\$ 6.470,00 = 2.670 + 2.850 + 950 ($ESCOLA)" || bad "escola: $ESCOLA"
api GET /internal/v1/modules escola-sis "$PILOTO_ESCOLA_TOKEN" >/dev/null
[ "$(field '.modules[] | select(.key=="attribution") | .integration.ready')" = true ] && ok "integração pronta" || bad "integração"

say "4. Reimportação"
load escola-sis PILOTO_ESCOLA_TOKEN escola-demo carga-1
[ "$(jq -r .replayed "$WORK/sum-receipts.json")" = 1 ] && ok "mesmo lote: resposta original, nada gravado" || bad "replay: $(cat "$WORK/sum-receipts.json")"
load escola-sis PILOTO_ESCOLA_TOKEN escola-demo carga-2
[ "$(jq -r '"\(.created) \(.updated) \(.unchanged)"' "$WORK/sum-receipts.json")" = "0 0 9" ] && ok "lote novo, mesmo arquivo: 9 recebimentos sem mudança" || bad "reimport: $(cat "$WORK/sum-receipts.json")"
[ "$(revenue escola-sis "$PILOTO_ESCOLA_TOKEN")" = "$ESCOLA" ] && ok "mesmos totais" || bad "totais mudaram"

say "5. Pausa e reativação"
config pausada; start_kas
CODE=$(api GET /internal/v1/reports/revenue escola-sis "$PILOTO_ESCOLA_TOKEN")
[ "$CODE $(field .error.code)" = "403 module_paused" ] && ok "pausado: 403 module_paused" || bad "pausa: $CODE $(cat "$WORK/out.json")"
config ativa; start_kas
[ "$(revenue escola-sis "$PILOTO_ESCOLA_TOKEN")" = "$ESCOLA" ] && ok "reativado: mesmos números, nada se perdeu" || bad "reativação"

say "6. Isolamento entre as empresas"
CODE=$(api GET "/internal/v1/attribution/revenue?tenant_id=fono-demo" escola-sis "$PILOTO_ESCOLA_TOKEN")
[ "$CODE $(field .error.code)" = "403 tenant_forbidden" ] && ok "escola não lê fono" || bad "escola leu fono: $CODE"
CODE=$(api GET "/internal/v1/reports/revenue?tenant_id=escola-demo" fono-crm "$PILOTO_FONO_TOKEN")
[ "$CODE $(field .error.code)" = "403 tenant_forbidden" ] && ok "fono não lê escola" || bad "fono leu escola: $CODE"
[ "$(revenue fono-crm "$PILOTO_FONO_TOKEN")" = "$FONO" ] && ok "fono-demo intacta depois de tudo" || bad "fono mudou"

say "Relatório entregue à empresa"
curl -sS -o "$WORK/relatorio-escola.html" -H "X-KAS-Service: escola-sis" -H "Authorization: Bearer $PILOTO_ESCOLA_TOKEN" \
  "$KAS/internal/v1/reports/revenue?from=2026-11-01&to=2026-11-30"
grep -q "Escola Fictícia Aurora" "$WORK/relatorio-escola.html" && grep -q "R\$ 6.470,00" "$WORK/relatorio-escola.html" \
  && ok "HTML com a marca da escola ($WORK/relatorio-escola.html)" || bad "HTML"

say "7. Nenhum código próprio da empresa nos módulos"
HITS=$(cd "$ROOT" && grep -rliE 'escola|fono|pacient|aluno|matr[ií]cula|mensalidade' \
  internal/attribution internal/modules internal/configstore/modules.go internal/httpapi/attribution.go \
  internal/httpapi/reports.go internal/httpapi/modules.go cmd/kas-attribution-import --include='*.go' | grep -v '_test.go' || true)
[ -z "$HITS" ] && ok "nenhum nome de empresa ou entidade de negócio" || bad "código específico em: $HITS"

say "Auditoria sem valores nem dados pessoais"
if psql "$MIGRATE_URL" -At -c "SELECT count(*) FROM audit_events WHERE tenant_id='escola-demo' AND action='attribution.batch_imported'" > "$WORK/audit.txt" 2>/dev/null; then
  [ "$(cat "$WORK/audit.txt")" -ge 8 ] && ok "$(cat "$WORK/audit.txt") lotes auditados" || bad "auditoria: $(cat "$WORK/audit.txt")"
fi
LEAK=$(psql "$MIGRATE_URL" -At -c "SELECT count(*) FROM attribution_items WHERE record::text ILIKE '%exemplo.test%' OR record::text LIKE '%99999%'")
[ "$LEAK" = 0 ] && ok "nenhum e-mail ou telefone bruto gravado" || bad "dado pessoal gravado: $LEAK"

if [ "$FAILS" = 0 ]; then say "TUDO OK"; else say "$FAILS FALHA(S)"; exit 1; fi
