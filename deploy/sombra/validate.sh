#!/usr/bin/env bash
# Validação isolada da SOMBRA do reaper KAS antes da sombra real (docs/event-reaper/SOMBRA.md).
#
# MongoDB 7 COM AUTENTICAÇÃO (replica set de 1 nó, keyfile) em loopback, dados fictícios.
# Cria o MESMO papel que será criado no Atlas (deploy/sombra/atlas-role.json) e prova:
#   S1  o servidor recusa escrita e leitura fora do eventstore para o usuário da sombra
#   S2  a sombra recusa iniciar com um usuário que pode escrever, ou sem usuário
#   S3  só a sombra rodando: o banco não muda (dbHash) e o oplog não tem nenhuma escrita
#   S4  sombra × reaper REAL do CRM (dono node, posse compartilhada ausente, como em produção):
#       conferências agree, nenhuma decision_mismatch; o resumo (kas-shadow-summary) passa
#   S5  sombra travada (SIGSTOP) e morta (kill -9): o Node recupera tudo sozinho e
#       não existe documento de posse da KAS
#   S6  banco pausado: a sombra registra erro no ciclo e volta sozinha
#
#   CRM_DIR=../fono-inova-crm-back deploy/sombra/validate.sh      (KEEP=1 mantém contêiner e logs)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
CRM_DIR=${CRM_DIR:-$(cd "$ROOT/.." && pwd)/fono-inova-crm-back}
MPORT=${MPORT:-27417}
M=kas-sombra-mongo
DB=isolado_crm
WORK=${WORK:-$(mktemp -d)}; mkdir -p "$WORK"
PIDS=(); FAILS=0
say() { printf '\n== %s\n' "$*"; }
ok() { printf 'OK: %s\n' "$*"; }
bad() { printf 'FALHOU: %s\n' "$*"; FAILS=$((FAILS + 1)); }
cleanup() {
  local rc=$?; set +e
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && { kill -CONT "$p" 2>/dev/null; kill -9 "$p" 2>/dev/null; }; done
  docker unpause "$M" >/dev/null 2>&1
  if [ "${KEEP:-0}" = 1 ]; then echo "mantidos em $WORK"; else docker rm -f "$M" >/dev/null 2>&1; fi
  exit "$rc"
}
trap cleanup EXIT
[ -f "$CRM_DIR/scripts/isolado/crmReaper.mjs" ] && [ -d "$CRM_DIR/node_modules" ] || { echo "CRM com npm ci não encontrado em $CRM_DIR"; exit 2; }

# Senhas descartáveis, geradas aqui e só para este contêiner.
ROOTPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9); APPPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
SHPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9); BADPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
H="127.0.0.1:${MPORT}"
ADMIN_URI="mongodb://root:${ROOTPW}@${H}/admin?directConnection=true"
CRM_URI="mongodb://crm_app:${APPPW}@${H}/${DB}?authSource=admin&directConnection=true"
SHADOW_URI="mongodb://kas_reaper_shadow:${SHPW}@${H}/?authSource=admin&directConnection=true"
BAD_URI="mongodb://kas_bad:${BADPW}@${H}/?authSource=admin&directConnection=true"
NOAUTH_URI="mongodb://${H}/?directConnection=true"
madmin() { docker exec "$M" mongosh --quiet "mongodb://root:${ROOTPW}@127.0.0.1:27017/admin?directConnection=true" --eval "$1"; }
mdb() { docker exec "$M" mongosh --quiet "mongodb://root:${ROOTPW}@127.0.0.1:27017/${DB}?authSource=admin&directConnection=true" --eval "$1"; }

crm() { # processo do CRM com ambiente limpo; só em segundo plano ou dentro de $( )
  local script=$1; shift
  exec env -i PATH="$PATH" HOME="$HOME" NODE_ENV=isolado TZ=America/Sao_Paulo CRM_ISOLADO_MONGO_URI="$CRM_URI" \
    ALERT_EMAIL_ENABLED=false SLACK_WEBHOOK_URL= WORKER_SIM_TOKENS="$WORK/tokens.json" "${CRM_ENV[@]}" node "$CRM_DIR/scripts/isolado/$script" "$@"
}
CRM_ENV=()
seed() { mdb "const d=[]; for (let i=0;i<$2;i++) d.push({eventId:'$1-'+i, eventType:'APPOINTMENT_CREATED', aggregateType:'appointment', aggregateId:'agg-'+i, payload:{nome:'PACIENTE-FICTICIO'}, status:'pending', attempts:0, createdAt:new Date()}); db.eventstore.insertMany(d).insertedIds.length" >/dev/null; }
abandon() { # worker REAL do CRM toma posse e morre com kill -9
  CRM_ENV=(EVENT_LEASE_MS=2000); crm crmWorkerSim.mjs claim "$1" "$2" 2000 >"$WORK/worker-$1.log" 2>&1 & local w=$!; CRM_ENV=()
  for _ in $(seq 1 100); do grep -q worker_sim_claimed "$WORK/worker-$1.log" 2>/dev/null && break; sleep 0.1; done
  kill -9 "$w" 2>/dev/null || true
}
count() { mdb "db.eventstore.countDocuments($1)"; }
recovered() { count "{eventId:/^$1-/, status:'pending', recoveryCount:1}"; }
wait_recovered() { for _ in $(seq 1 "$3"); do [ "$(recovered "$1")" = "$2" ] && return 0; sleep 1; done; return 1; }
shadow_start() { # $1 = nome, $2 = URI; demais = flags
  local n=$1 uri=$2; shift 2
  KAS_REAPER_MONGO_URI="$uri" "$WORK/kas-event-reaper" -mode shadow -protocol v2 -db "$DB" -require-read-only on \
    -interval 2s -warmup 0s -cycle-timeout 3s -report "$WORK/$n.jsonl" "$@" >"$WORK/$n.log" 2>&1 & SH_PID=$!
  PIDS+=("$SH_PID")
}
node_start() { # reaper REAL do CRM, dono node, EVENT_REAPER_SHARED_LEASE ausente (produção hoje)
  CRM_ENV=(EVENT_REAPER_OWNER=node EVENT_REAPER_INTERVAL_MS=1000 EVENT_REAPER_WARMUP_MS=200 EVENT_LEASE_MS=2000)
  crm crmReaper.mjs >>"$WORK/node.log" 2>&1 & NODE_PID=$!; CRM_ENV=(); PIDS+=("$NODE_PID")
}

say "build"
(cd "$ROOT" && go build -o "$WORK/kas-event-reaper" ./cmd/kas-event-reaper && go build -o "$WORK/kas-shadow-summary" ./cmd/kas-shadow-summary)

say "MongoDB 7 com autenticação (replica set + keyfile) em $H, descartável"
docker rm -f "$M" >/dev/null 2>&1 || true
docker run -d --name "$M" -p "${H}:27017" --entrypoint bash mongo:7 -c \
  'head -c 756 /dev/urandom | base64 > /tmp/k && chmod 400 /tmp/k && chown mongodb /tmp/k && exec docker-entrypoint.sh mongod --replSet rs0 --keyFile /tmp/k --bind_ip_all' >/dev/null
for _ in $(seq 1 40); do docker exec "$M" mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$M" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]}).ok' >/dev/null
for _ in $(seq 1 30); do [ "$(docker exec "$M" mongosh --quiet --eval 'db.hello().isWritablePrimary')" = true ] && break; sleep 1; done
docker exec "$M" mongosh --quiet admin --eval "db.createUser({user:'root', pwd:'${ROOTPW}', roles:['root']}).ok" >/dev/null
# Papel da sombra: o MESMO JSON que vai para o Atlas, com o nome do banco trocado.
ROLE=$(sed "s/fono_inova_prod/${DB}/" "$HERE/atlas-role.json")
madmin "const r = ${ROLE}; db.createRole({role: r.roleName, privileges: r.actions.flatMap(a => a.resources.map(res => ({resource: {db: res.db, collection: res.collection}, actions: [a.action.toLowerCase()]}))), roles: r.inheritedRoles}).ok" >/dev/null
madmin "db.createUser({user:'kas_reaper_shadow', pwd:'${SHPW}', roles:[{role:'kasReaperShadowRead', db:'admin'}]}).ok" >/dev/null
madmin "db.createUser({user:'crm_app', pwd:'${APPPW}', roles:[{role:'readWrite', db:'${DB}'}]}).ok" >/dev/null
madmin "db.createUser({user:'kas_bad', pwd:'${BADPW}', roles:[{role:'readWrite', db:'${DB}'}]}).ok" >/dev/null
mdb "db.createCollection('eventstore'); db.eventstore.createIndex({eventId:1},{unique:true}); db.eventstore.createIndex({status:1, createdAt:1}); db.createCollection('kas_event_reaper_lease'); db.createCollection('patients'); db.patients.insertOne({nome:'PACIENTE-FICTICIO'}); 1" >/dev/null
ok "usuários: root, crm_app (readWrite), kas_reaper_shadow (papel kasReaperShadowRead), kas_bad (readWrite)"

say "S1  o servidor recusa o que a sombra não deve fazer"
shm() { docker exec "$M" mongosh --quiet "mongodb://kas_reaper_shadow:${SHPW}@127.0.0.1:27017/${DB}?authSource=admin&directConnection=true" --eval "$1" 2>&1 || true; }
shm "db.eventstore.insertOne({eventId:'x'})" | grep -qi "not authorized\|Unauthorized" && ok "insert no eventstore: recusado" || bad "insert do usuário da sombra não foi recusado"
shm "db.eventstore.updateOne({}, {\$set:{status:'x'}})" | grep -qi "not authorized\|Unauthorized" && ok "update no eventstore: recusado" || bad "update não recusado"
shm "db.kas_event_reaper_lease.insertOne({_id:'x'})" | grep -qi "not authorized\|Unauthorized" && ok "posse (kas_event_reaper_lease): escrita recusada" || bad "escrita na posse não recusada"
shm "db.patients.findOne()" | grep -qi "not authorized\|Unauthorized" && ok "leitura fora do eventstore (patients): recusada" || bad "usuário da sombra lê outra coleção"
shm "db.eventstore.countDocuments({})" | grep -qE '^[0-9]+$' && ok "leitura do eventstore: permitida" || bad "usuário da sombra não lê o eventstore"

say "S2  a sombra recusa iniciar sem prova de só leitura"
KAS_REAPER_MONGO_URI="$BAD_URI" "$WORK/kas-event-reaper" -mode shadow -protocol v2 -db "$DB" -require-read-only on -once >"$WORK/s2-bad.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 2 ] && grep -q shadow_refused_not_read_only "$WORK/s2-bad.log" && ok "usuário com readWrite: recusa (código 2), nenhum ciclo" || { bad "aceitou usuário que escreve (rc=$rc)"; cat "$WORK/s2-bad.log"; }
KAS_REAPER_MONGO_URI="$NOAUTH_URI" "$WORK/kas-event-reaper" -mode shadow -protocol v2 -db "$DB" -require-read-only on -once >"$WORK/s2-noauth.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 2 ] && grep -q "no authenticated user" "$WORK/s2-noauth.log" && ok "sem usuário autenticado: recusa" || { bad "aceitou conexão sem usuário (rc=$rc)"; cat "$WORK/s2-noauth.log"; }
KAS_REAPER_MONGO_URI="$SHADOW_URI" "$WORK/kas-event-reaper" -mode shadow -protocol v2 -db "$DB" -require-read-only on -once -report "$WORK/s2-ok.jsonl" >"$WORK/s2-ok.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 0 ] && grep -q read_only_confirmed "$WORK/s2-ok.log" && grep -q '"actions":\["find"\]' "$WORK/s2-ok.jsonl" && ok "usuário da sombra: read_only_confirmed, ações = [find]" || { bad "usuário da sombra não confirmou (rc=$rc)"; cat "$WORK/s2-ok.log"; }

say "S3  só a sombra rodando: banco intacto e oplog sem escrita"
seed f 60; abandon f 40
sleep 3 # posses vencem: 40 elegíveis para a sombra
H0=$(madmin "db.getSiblingDB('${DB}').runCommand({dbHash:1}).md5")
T0=$(madmin 'const t=db.hello().operationTime; JSON.stringify({t:t.t,i:t.i})')
shadow_start s3 "$SHADOW_URI"; S3=$SH_PID
sleep 9
kill -TERM "$S3"; wait "$S3" 2>/dev/null || true
H1=$(madmin "db.getSiblingDB('${DB}').runCommand({dbHash:1}).md5")
OPS=$(madmin "const t=${T0}; db.getSiblingDB('local').oplog.rs.countDocuments({ts:{\$gt: Timestamp(t.t, t.i)}, ns: /^${DB}\\./})")
PRED=$(grep -o '"result":"predicted"' "$WORK/s3.jsonl" | wc -l)
[ "$H0" = "$H1" ] && ok "dbHash igual antes e depois ($H0)" || bad "dbHash mudou com só a sombra rodando"
[ "$OPS" = 0 ] && ok "oplog: 0 escritas no banco durante a sombra" || bad "oplog com $OPS escritas"
[ "$PRED" -ge 40 ] && ok "a sombra previu $PRED transições (40 eventos elegíveis, vários ciclos) sem gravar" || bad "sombra previu só $PRED"

say "S4  sombra × reaper REAL do CRM (dono node, como em produção)"
shadow_start s4 "$SHADOW_URI"; S4=$SH_PID
sleep 1
node_start; NODE=$NODE_PID
wait_recovered f 40 40 && ok "Node recuperou 40/40 do lote f" || bad "Node: $(recovered f)/40"
seed g 50; abandon g 50
wait_recovered g 50 40 && ok "Node recuperou 50/50 do lote g com a sombra ao lado" || bad "Node: $(recovered g)/50"
sleep 6 # a sombra confere as previsões do último ciclo
kill -TERM "$S4"; wait "$S4" 2>/dev/null || true
set +e; "$WORK/kas-shadow-summary" -min-days 0 -json-out "$WORK/s4-summary.json" "$WORK/s4.jsonl" >"$WORK/s4-summary.md"; SRC=$?; set -e
cat "$WORK/s4-summary.md"
AGREE=$(sed -n 's/.*"agree": \([0-9]*\).*/\1/p' "$WORK/s4-summary.json" | head -1)
grep -q '"decision_mismatch"' "$WORK/s4-summary.json" && bad "houve decision_mismatch" || ok "nenhuma decision_mismatch"
[ "${AGREE:-0}" -ge 1 ] && ok "conferências agree: $AGREE" || bad "nenhuma conferência agree"
[ "$SRC" = 0 ] && ok "kas-shadow-summary: todos os critérios passam (período parcial, -min-days 0)" || bad "resumo com critério reprovado (código $SRC)"

say "S5  sombra travada e morta não afeta o Node"
shadow_start s5a "$SHADOW_URI"; S5A=$SH_PID
shadow_start s5b "$SHADOW_URI"; S5B=$SH_PID
sleep 3
kill -STOP "$S5A"; kill -9 "$S5B"
seed h 60; abandon h 60
wait_recovered h 60 40 && ok "Node recuperou 60/60 com uma sombra travada (SIGSTOP) e outra morta (kill -9)" || bad "Node: $(recovered h)/60"
kill -0 "$NODE" 2>/dev/null && ok "processo do Node seguiu de pé" || bad "Node caiu"
LEASES=$(mdb "db.kas_event_reaper_lease.countDocuments({holder: {\$not: /^crm-node/}})")
[ "$LEASES" = 0 ] && ok "nenhum documento de posse da KAS: a sombra nunca disputa a posse" || bad "$LEASES documento(s) de posse fora do Node"
grep -q '"msg":"crm_reaper_boot"' "$WORK/node.log" && ! grep -qi 'posse.*kas\|skip.*kas' "$WORK/node.log" && ok "log do Node sem ciclo pulado por posse da KAS" || echo "aviso: conferir $WORK/node.log"
kill -CONT "$S5A"; kill -9 "$S5A" 2>/dev/null || true

say "S6  banco pausado: a sombra registra erro e volta sozinha"
shadow_start s6 "$SHADOW_URI"; S6=$SH_PID
sleep 3; docker pause "$M" >/dev/null; sleep 7; docker unpause "$M" >/dev/null; sleep 8
kill -TERM "$S6"; wait "$S6" 2>/dev/null || true
ERRS=$(grep -c '"error":"' "$WORK/s6.jsonl" || true)
LASTOK=$(awk '/"kind":"cycle"/ { if (/"error":"/) e=1; else if (e) n++ } END { print n+0 }' "$WORK/s6.jsonl")
STOPERR=$(grep -c 'context canceled' "$WORK/s6.jsonl" || true)
[ "$STOPERR" = 0 ] && ok "parada com SIGTERM não vira ciclo com erro" || bad "SIGTERM gerou ciclo com erro falso"
[ "$ERRS" -ge 1 ] && ok "ciclos com erro registrados no relatório durante a pausa: $ERRS" || bad "pausa não apareceu no relatório"
[ "$LASTOK" -ge 1 ] && ok "$LASTOK ciclo(s) sem erro depois da volta do banco" || bad "sombra não voltou depois da pausa"
kill -0 "$NODE" 2>/dev/null && ok "Node de pé depois da pausa" || bad "Node caiu na pausa"
kill -TERM "$NODE" 2>/dev/null || true

echo
[ "$FAILS" = 0 ] && echo "TUDO OK" || { echo "FALHAS: $FAILS (logs em $WORK)"; exit 1; }
