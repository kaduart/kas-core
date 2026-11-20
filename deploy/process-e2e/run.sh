#!/usr/bin/env bash
# Teste integrado do Process Studio, isolado e descartável.
#
# Sobe o executável REAL da KAS com -process-store postgres (papel de runtime
# sem DDL, grants de deploy/grants.sql + deploy/grants-process.sql) e o CRM
# simulado (cmd/kas-crm-sim) consumindo comandos pelo pull REAL do Outbox.
# Tenants, atores e contratos fictícios. Nada fala com Render, Atlas, CRM real
# ou WhatsApp; tudo em loopback.
#
#   deploy/process-e2e/run.sh                    # Postgres 16 descartável em Docker
#   PG_ADMIN_URL=postgres://kas@127.0.0.1:5433/postgres?sslmode=disable deploy/process-e2e/run.sh
#   KEEP=1 deploy/process-e2e/run.sh             # mantém logs e banco para inspeção
#
# Cenários: (1) CRM cai depois do efeito e antes do resultado; KAS cai com
# kill -9 e volta; o CRM reinicia, recebe de novo, não repete o efeito e a
# instância termina. (2) Isolamento entre as duas empresas. (3) Nova versão
# publicada: instância antiga mantém o prazo original. (4) Timer de prazo
# dispara no executável. (5) Recusa do CRM leva à revisão.
# Só termina com "TUDO OK" se todos passarem.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$(mktemp -d)}; mkdir -p "$WORK"
PORT=${PORT:-18188}
DBPORT=${DBPORT:-55432}
DBNAME=kas_process_e2e
RT_ROLE=kas_process_e2e_rt
PG=kas-process-e2e-pg
KAS="http://127.0.0.1:$PORT"
KAS_PID=""
FAILS=0

say() { printf '\n== %s\n' "$*"; }
ok() { printf 'OK: %s\n' "$*"; }
bad() { printf 'FALHOU: %s\n' "$*"; FAILS=$((FAILS + 1)); }
cleanup() {
  local rc=$?
  set +e
  [ -n "$KAS_PID" ] && kill -9 "$KAS_PID" 2>/dev/null
  if [ "${KEEP:-0}" = 1 ]; then echo "logs mantidos em $WORK"; else
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
(cd "$ROOT" && go build -o "$WORK/kas-core" ./cmd/kas-core && go build -o "$WORK/kas-crm-sim" ./cmd/kas-crm-sim)

say "Migrações com o papel de migração e grants"
KAS_AUDIT_MIGRATION_DATABASE_URL="$MIGRATE_URL" KAS_EVENTS_MIGRATION_DATABASE_URL="$MIGRATE_URL" \
  "$WORK/kas-core" -audit-store postgres -audit-migrate-only -events-store postgres -events-migrate-only \
  -process-store postgres -process-migrate-only > "$WORK/migrate.log"
psql "$MIGRATE_URL" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants.sql"
psql "$MIGRATE_URL" -q -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=$RT_ROLE -f "$ROOT/deploy/grants-process.sql"
ok "migrações e grants aplicados"

TOKEN_A=$(openssl rand -hex 32); TOKEN_B=$(openssl rand -hex 32)
export DEMO_CRM_A_TOKEN=$TOKEN_A DEMO_CRM_B_TOKEN=$TOKEN_B
TYPES='"process.command.requested","process.command.status_query","process.wait.opened"'
CAPS='"events:consume","process:define","process:run","process:task","process:command","process:event"'
cat > "$WORK/config.json" <<JSON
{"contract_version":"v1","config_version":"process-e2e",
 "tenant_registry":[{"id":"demo-empresa-a","status":"active"},{"id":"demo-empresa-b","status":"active"}],
 "key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{},
 "event_types":[$TYPES],
 "event_consumers":{
  "crm-simulado-a":{"service":"demo-crm-a","event_types":[$TYPES],"tenants":["demo-empresa-a"]},
  "crm-simulado-b":{"service":"demo-crm-b","event_types":[$TYPES],"tenants":["demo-empresa-b"]}},
 "callers":{
  "demo-crm-a":{"token_env":"DEMO_CRM_A_TOKEN","tenants":["demo-empresa-a"],"capabilities":[$CAPS]},
  "demo-crm-b":{"token_env":"DEMO_CRM_B_TOKEN","tenants":["demo-empresa-b"],"capabilities":[$CAPS]}}}
JSON

start_kas() {
  KAS_EVENTS_DATABASE_URL="$RUNTIME_URL" KAS_AUDIT_DATABASE_URL="$RUNTIME_URL" "$WORK/kas-core" -config "$WORK/config.json" \
    -audit-store postgres -audit-auto-migrate=false -events-store postgres -events-auto-migrate=false -process-store postgres -process-auto-migrate=false \
    -addr 127.0.0.1:$PORT >> "$WORK/kas.log" 2>&1 &
  KAS_PID=$!
  for i in $(seq 1 50); do curl -fsS "$KAS/readyz" >/dev/null 2>&1 && return 0; sleep 0.2; done
  echo "KAS não ficou pronta"; tail -20 "$WORK/kas.log"; exit 1
}

# api METHOD PATH SERVICE TOKEN [BODY] -> grava corpo em $WORK/out.json e devolve o status HTTP
api() {
  local body=${5:-}
  local args=(-sS -o "$WORK/out.json" -w '%{http_code}' -X "$1" -H "X-KAS-Service: $3" -H "Authorization: Bearer $4")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data "$body")
  curl "${args[@]}" "$KAS$2"
}
must() { local code; code=$(api "$@"); case "$code" in 2*) ;; *) echo "$1 $2 -> $code $(cat "$WORK/out.json")"; exit 1;; esac; }
now() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }
field() { jq -r "$1" "$WORK/out.json"; }

retention() { # $1 = dias úteis
  cat <<JSON
{"schema":"kas.process/v1","process_key":"retencao-cancelamento","start":"negociar",
 "calendar":{"zone":"America/Sao_Paulo","holidays":["2026-12-25"]},
 "steps":{
  "negociar":{"type":"human_task","roles":["comercial"],"outcomes":["cliente_permanece","cliente_cancela"],
    "deadline":{"business_days":$1,"cutoff":"23:59"},"grace":"PT5M","on_deadline":"revisar","late_response":"record_only","next":"resultado"},
  "resultado":{"type":"decision","routes":{"cliente_permanece":"fim_retido","cliente_cancela":"pedir_cancelamento"}},
  "pedir_cancelamento":{"type":"external_command","command_type":"crm.contract.cancel","reply_timeout":"PT1H","max_status_queries":2,
    "on_confirmed":"fim_cancelado","on_rejected":"revisar","on_no_reply":"revisar","business_status":"910"},
  "revisar":{"type":"human_task","roles":["supervisor"],"outcomes":["cliente_permanece","cliente_cancela"],"next":"resultado"},
  "fim_retido":{"type":"end","result":"retido","business_status":"220"},
  "fim_cancelado":{"type":"end","result":"cancelado"}}}
JSON
}
QUICK='{"schema":"kas.process/v1","process_key":"retorno-rapido","start":"ligar",
 "steps":{"ligar":{"type":"human_task","roles":["comercial"],"outcomes":["feito"],"deadline":{"duration":"PT2S"},"grace":"PT1S",
   "on_deadline":"fim_expirado","late_response":"record_only","next":"fim_ok"},
  "fim_ok":{"type":"end","result":"ok"},"fim_expirado":{"type":"end","result":"expirado","business_status":"305"}}}'

publish() { # service token publish_id definition
  must POST /internal/v1/process/definitions "$1" "$2" "{\"contract_version\":\"v1\",\"publish_id\":\"$3\",\"definition\":$4}"
}
roles() { # service token
  local from; from=$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)
  must PUT /internal/v1/process/roles/comercial/members "$1" "$2" "{\"contract_version\":\"v1\",\"change_id\":\"chg-com\",\"members\":[\"ator-comercial-1\"],\"valid_from\":\"$from\"}"
  must PUT /internal/v1/process/roles/supervisor/members "$1" "$2" "{\"contract_version\":\"v1\",\"change_id\":\"chg-sup\",\"members\":[\"ator-supervisor-1\"],\"valid_from\":\"$from\"}"
}
start() { # service token process_key ref -> instance id
  must POST /internal/v1/process/instances "$1" "$2" "{\"contract_version\":\"v1\",\"process_key\":\"$3\",\"business_ref\":\"$4\",\"start_key\":\"start-$4\",\"actor_id\":\"ator-comercial-1\",\"occurred_at\":\"$(now)\"}"
  field .instance_id
}
inst() { must GET "/internal/v1/process/instances/$3" "$1" "$2"; }
open_task() { inst "$1" "$2" "$3"; field '[.instance.tasks[] | select(.status=="open")][0].id'; }
resolve() { # service token task outcome role actor key
  must POST "/internal/v1/process/tasks/$3/resolve" "$1" "$2" "{\"contract_version\":\"v1\",\"submission_key\":\"$7\",\"outcome\":\"$4\",\"actor_id\":\"$6\",\"role\":\"$5\",\"occurred_at\":\"$(now)\"}"
}
sim() { # tenant-letter extra-args...
  local l=$1; shift
  local svc=demo-crm-$l up; up=$(echo "$l" | tr a-z A-Z)
  "$WORK/kas-crm-sim" -kas "$KAS" -service "$svc" -token-env "DEMO_CRM_${up}_TOKEN" -consumer "crm-simulado-$l" \
    -state "$WORK/sim-$l.json" -lease-seconds 5 "$@" >> "$WORK/sim-$l.log" 2>&1
}

say "KAS com -process-store postgres (papel de runtime, sem auto-migração)"
start_kas
ok "readyz ok (pid $KAS_PID)"
publish demo-crm-a "$TOKEN_A" pub-a-v1 "$(retention 2)"; roles demo-crm-a "$TOKEN_A"
publish demo-crm-a "$TOKEN_A" pub-a-quick "$QUICK"
publish demo-crm-b "$TOKEN_B" pub-b-v1 "$(retention 5)"; roles demo-crm-b "$TOKEN_B"
ok "demo-empresa-a (2 dias úteis) e demo-empresa-b (5 dias úteis) publicadas"

say "1. CRM cai depois do efeito; KAS cai com kill -9; tudo volta sem efeito duplicado"
ID1=$(start demo-crm-a "$TOKEN_A" retencao-cancelamento contrato-demo-1)
resolve demo-crm-a "$TOKEN_A" "$(open_task demo-crm-a "$TOKEN_A" "$ID1")" cliente_cancela comercial ator-comercial-1 sub-1
set +e; sim a -once -crash-after-effect; rc=$?; set -e
[ "$rc" = 3 ] && ok "CRM simulado caiu depois do efeito (código 3)" || bad "CRM simulado deveria cair com 3, saiu $rc"
inst demo-crm-a "$TOKEN_A" "$ID1"
[ "$(field .instance.status)" = running ] && ok "instância segue esperando o resultado" || bad "instância andou sem resultado: $(field .instance.status)"
kill -9 "$KAS_PID"; wait "$KAS_PID" 2>/dev/null || true
start_kas
ok "KAS reiniciada depois de kill -9"
for i in $(seq 1 40); do sim a -once; inst demo-crm-a "$TOKEN_A" "$ID1"; [ "$(field .instance.status)" = completed ] && break; sleep 0.5; done
[ "$(field .instance.result)" = cancelado ] && ok "instância 1 terminou: cancelado" || bad "instância 1: $(field .instance.status) $(field .instance.result)"
EFFECTS=$(jq '[.effects[]] | add' "$WORK/sim-a.json")
[ "$EFFECTS" = 1 ] && ok "efeito aplicado uma vez só" || bad "efeito aplicado $EFFECTS vezes"

say "2. Isolamento entre empresas"
code=$(api GET "/internal/v1/process/instances/$ID1" demo-crm-b "$TOKEN_B")
[ "$code" = 404 ] && ok "empresa B não vê instância da A (404)" || bad "B leu instância da A: $code"
code=$(api POST /internal/v1/process/instances demo-crm-b "$TOKEN_B" "{\"contract_version\":\"v1\",\"tenant_id\":\"demo-empresa-a\",\"process_key\":\"retencao-cancelamento\",\"business_ref\":\"x\",\"start_key\":\"x\",\"actor_id\":\"ator-comercial-1\",\"occurred_at\":\"$(now)\"}")
[ "$code" = 403 ] && [ "$(field .error.code)" = tenant_forbidden ] && ok "empresa B não age em nome da A (403)" || bad "B agiu como A: $code"

say "3. Nova versão publicada: instância antiga mantém o prazo original"
ID2=$(start demo-crm-a "$TOKEN_A" retencao-cancelamento contrato-demo-2)
inst demo-crm-a "$TOKEN_A" "$ID2"; DUE_OLD=$(field '.instance.tasks[0].due_at'); V_OLD=$(field .instance.version)
publish demo-crm-a "$TOKEN_A" pub-a-v2 "$(retention 3)"
[ "$(field .version)" = 2 ] && ok "versão 2 publicada (3 dias úteis)" || bad "versão publicada: $(field .version)"
ID3=$(start demo-crm-a "$TOKEN_A" retencao-cancelamento contrato-demo-3)
inst demo-crm-a "$TOKEN_A" "$ID2"; DUE_OLD_AFTER=$(field '.instance.tasks[0].due_at')
inst demo-crm-a "$TOKEN_A" "$ID3"; DUE_NEW=$(field '.instance.tasks[0].due_at'); V_NEW=$(field .instance.version)
[ "$V_OLD" = 1 ] && [ "$V_NEW" = 2 ] && ok "instância antiga na v1, nova na v2" || bad "versões: antiga $V_OLD, nova $V_NEW"
[ "$DUE_OLD" = "$DUE_OLD_AFTER" ] && ok "prazo da instância antiga não mudou ($DUE_OLD)" || bad "prazo antigo mudou: $DUE_OLD -> $DUE_OLD_AFTER"
[[ "$DUE_NEW" > "$DUE_OLD" ]] && ok "instância nova com prazo maior ($DUE_NEW)" || bad "prazo novo $DUE_NEW não é maior que $DUE_OLD"

say "4. Timer de prazo dispara no executável (prazo 2 s + tolerância 1 s + 30 s de assentamento)"
ID4=$(start demo-crm-a "$TOKEN_A" retorno-rapido ligacao-demo-1)
for i in $(seq 1 60); do inst demo-crm-a "$TOKEN_A" "$ID4"; [ "$(field .instance.status)" = completed ] && break; sleep 1; done
[ "$(field .instance.result)" = expirado ] && ok "prazo vencido levou ao fim 'expirado' sem ninguém agir" || bad "timer: $(field .instance.status) $(field .instance.result)"

say "5. Recusa do CRM leva à revisão (empresa B)"
ID5=$(start demo-crm-b "$TOKEN_B" retencao-cancelamento contrato-b-1)
resolve demo-crm-b "$TOKEN_B" "$(open_task demo-crm-b "$TOKEN_B" "$ID5")" cliente_cancela comercial ator-comercial-1 sub-b-1
sim b -once -reject-refs contrato-b-1
inst demo-crm-b "$TOKEN_B" "$ID5"
[ "$(field .instance.current_step)" = revisar ] && ok "recusa levou à revisão do supervisor" || bad "B depois da recusa: $(field .instance.current_step)"
[ ! -s "$WORK/sim-b.json" ] || [ "$(jq '.receipts | length' "$WORK/sim-b.json")" = 1 ] && ok "CRM B só recebeu o comando da própria empresa" || bad "CRM B recebeu comandos de outra empresa"

say "Resultado"
if [ "$FAILS" -eq 0 ]; then echo "TUDO OK"; else echo "$FAILS falha(s)"; exit 1; fi
