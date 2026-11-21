#!/usr/bin/env bash
# Validação isolada da SOMBRA da recuperação do Outbox (failed) antes de qualquer uso em produção
# (docs/outbox-recovery/SOMBRA.md). TESTE COM DADOS FICTÍCIOS: os números daqui não dizem nada
# sobre a produção.
#
# MongoDB 7 COM AUTENTICAÇÃO (replica set de 1 nó, keyfile) e Redis 7, ambos em loopback.
# Cria o MESMO papel que será criado no Atlas (deploy/sombra-outbox/atlas-role.json) e prova:
#   T1  o servidor nega ao usuário da sombra escrita no outboxes e leitura fora dele
#   T2  a sombra recusa iniciar com usuário que escreve ou sem usuário; com o usuário dela, só [find]
#   T3  só a sombra rodando: banco intacto (dbHash), 0 escritas no oplog; relatório e resumo com
#       a contagem exata esperada por tipo, ação, destino, tentativas e idade
#   T4  dispatcher REAL do CRM ao lado (Redis): publica só as pending; nenhum job no Redis para
#       entradas failed; nenhuma failed muda; nenhuma anotação kasRecovery
#   T5  sombra travada (SIGSTOP) e morta (kill -9): o dispatcher segue publicando
#   T6  banco pausado: a sombra registra erro no ciclo e volta sozinha
#
#   CRM_DIR=../fono-inova-crm-back deploy/sombra-outbox/validate.sh   (KEEP=1 mantém contêineres e logs)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
CRM_DIR=${CRM_DIR:-$(cd "$ROOT/.." && pwd)/fono-inova-crm-back}
MPORT=${MPORT:-27517}
RPORT=${RPORT:-6679}
M=kas-sombra-outbox-mongo
R=kas-sombra-outbox-redis
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
  if [ "${KEEP:-0}" = 1 ]; then echo "mantidos em $WORK"; else docker rm -f "$M" "$R" >/dev/null 2>&1; fi
  exit "$rc"
}
trap cleanup EXIT
[ -f "$CRM_DIR/scripts/isolado/crmDispatcher.mjs" ] && [ -d "$CRM_DIR/node_modules" ] || { echo "CRM com npm ci não encontrado em $CRM_DIR"; exit 2; }

ROOTPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9); APPPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
SHPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9); BADPW=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
H="127.0.0.1:${MPORT}"
CRM_URI="mongodb://crm_app:${APPPW}@${H}/${DB}?authSource=admin&directConnection=true"
SHADOW_URI="mongodb://kas_outbox_shadow:${SHPW}@${H}/?authSource=admin&directConnection=true"
BAD_URI="mongodb://kas_bad:${BADPW}@${H}/?authSource=admin&directConnection=true"
NOAUTH_URI="mongodb://${H}/?directConnection=true"
madmin() { docker exec "$M" mongosh --quiet "mongodb://root:${ROOTPW}@127.0.0.1:27017/admin?directConnection=true" --eval "$1"; }
mdb() { docker exec "$M" mongosh --quiet "mongodb://root:${ROOTPW}@127.0.0.1:27017/${DB}?authSource=admin&directConnection=true" --eval "$1"; }
shadow_start() { # $1 = nome, $2 = URI; demais = flags
  local n=$1 uri=$2; shift 2
  KAS_OUTBOX_MONGO_URI="$uri" "$WORK/kas-outbox-recovery" -mode shadow -db "$DB" -require-read-only on \
    -interval 2s -cycle-timeout 3s -report "$WORK/$n.jsonl" "$@" >"$WORK/$n.log" 2>&1 & SH_PID=$!
  PIDS+=("$SH_PID")
}
dispatcher_start() {
  env -i PATH="$PATH" HOME="$HOME" NODE_ENV=isolado TZ=America/Sao_Paulo CRM_ISOLADO_MONGO_URI="$CRM_URI" \
    REDIS_URL="redis://127.0.0.1:${RPORT}" DISPATCHER_INTERVAL_MS=300 ALERT_EMAIL_ENABLED=false SLACK_WEBHOOK_URL= \
    node "$CRM_DIR/scripts/isolado/crmDispatcher.mjs" >>"$WORK/dispatcher.log" 2>&1 & DISP=$!
  PIDS+=("$DISP")
}
failed_hash() { mdb 'const c=require("crypto").createHash("md5"); db.outboxes.find({eventId:/^f-/}).sort({_id:1}).forEach(d=>c.update(JSON.stringify(d))); c.digest("hex")'; }
redis_jobs() { docker exec "$R" redis-cli --scan --pattern "bull:*:$1*__*" | grep -vc ':lock' || true; }

# Entradas FICTÍCIAS, só para teste. Ficam como failed (o dispatcher do CRM nunca as pega de novo):
#   12 APPOINTMENT_CREATED  tentativa 2, 30 h     -> recuperaria; destino notification + 2 filas
#    6 PATIENT_UPDATED      tentativa 1, 20 min   -> recuperaria; destino patient-projection
#    4 SESSION_COMPLETED    tentativa 5, 9 dias   -> desistiria (attempts_exhausted)
#    3 APPOINTMENT_CONFIRMED UNKNOWN_EVENT_TYPE   -> desistiria (permanent_error), sem rota
#    2 PAYMENT_STATUS_CHANGED backoff no futuro   -> aguardaria
seed_failed() {
  mdb "
    const now = Date.now(), d = [];
    const add = (n, ty, att, ageMs, err, schedMs) => { for (let i = 0; i < n; i++) d.push({eventId: 'f-' + ty + '-' + i, correlationId: 'c', eventType: ty,
      aggregateType: 'x', aggregateId: 'agg-' + i, payload: {nome: 'PACIENTE-FICTICIO'}, status: 'failed', attempts: att, lastError: err,
      scheduledAt: new Date(now + schedMs), createdAt: new Date(now - ageMs)}); };
    add(12, 'APPOINTMENT_CREATED', 2, 30*3600e3, 'Connection is closed.', -60e3);
    add(6, 'PATIENT_UPDATED', 1, 20*60e3, 'Connection is closed.', -60e3);
    add(4, 'SESSION_COMPLETED', 5, 9*86400e3, 'Connection is closed.', -60e3);
    add(3, 'APPOINTMENT_CONFIRMED', 1, 3600e3, 'UNKNOWN_EVENT_TYPE: APPOINTMENT_CONFIRMED não está em eventToQueueMap', -60e3);
    add(2, 'PAYMENT_STATUS_CHANGED', 1, 600e3, 'Connection is closed.', 3600e3);
    db.outboxes.insertMany(d).insertedIds.length" >/dev/null
}
seed_pending() { # $1 prefixo, $2 n: pending prontos para o dispatcher
  mdb "const now = Date.now(), d = []; for (let i = 0; i < $2; i++) d.push({eventId: '$1-' + i, correlationId: 'c', eventType: 'PATIENT_UPDATED',
    aggregateType: 'x', aggregateId: 'p-' + i, payload: {nome: 'PACIENTE-FICTICIO'}, status: 'pending', attempts: 0, scheduledAt: new Date(now - 1000), createdAt: new Date(now)});
    db.outboxes.insertMany(d).insertedIds.length" >/dev/null
}
published() { mdb "db.outboxes.countDocuments({eventId: /^$1-/, status: 'published'})"; }
wait_published() { for _ in $(seq 1 "$3"); do [ "$(published "$1")" = "$2" ] && return 0; sleep 1; done; return 1; }

say "build"
(cd "$ROOT" && go build -o "$WORK/kas-outbox-recovery" ./cmd/kas-outbox-recovery && go build -o "$WORK/kas-outbox-shadow-summary" ./cmd/kas-outbox-shadow-summary)

say "MongoDB 7 com autenticação em $H e Redis 7 em 127.0.0.1:${RPORT}, descartáveis"
docker rm -f "$M" "$R" >/dev/null 2>&1 || true
docker run -d --name "$M" -p "${H}:27017" --entrypoint bash mongo:7 -c \
  'head -c 756 /dev/urandom | base64 > /tmp/k && chmod 400 /tmp/k && chown mongodb /tmp/k && exec docker-entrypoint.sh mongod --replSet rs0 --keyFile /tmp/k --bind_ip_all' >/dev/null
docker run -d --name "$R" -p "127.0.0.1:${RPORT}:6379" redis:7-alpine >/dev/null
for _ in $(seq 1 40); do docker exec "$M" mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$M" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]}).ok' >/dev/null
for _ in $(seq 1 30); do [ "$(docker exec "$M" mongosh --quiet --eval 'db.hello().isWritablePrimary')" = true ] && break; sleep 1; done
for _ in $(seq 1 30); do docker exec "$R" redis-cli ping >/dev/null 2>&1 && break; sleep 1; done
docker exec "$M" mongosh --quiet admin --eval "db.createUser({user:'root', pwd:'${ROOTPW}', roles:['root']}).ok" >/dev/null
ROLE=$(sed "s/fono_inova_prod/${DB}/" "$HERE/atlas-role.json")
madmin "const r = ${ROLE}; db.createRole({role: r.roleName, privileges: r.actions.flatMap(a => a.resources.map(res => ({resource: {db: res.db, collection: res.collection}, actions: [a.action.toLowerCase()]}))), roles: r.inheritedRoles}).ok" >/dev/null
madmin "db.createUser({user:'kas_outbox_shadow', pwd:'${SHPW}', roles:[{role:'kasOutboxShadowRead', db:'admin'}]}).ok" >/dev/null
madmin "db.createUser({user:'crm_app', pwd:'${APPPW}', roles:[{role:'readWrite', db:'${DB}'}]}).ok" >/dev/null
madmin "db.createUser({user:'kas_bad', pwd:'${BADPW}', roles:[{role:'readWrite', db:'${DB}'}]}).ok" >/dev/null
mdb "db.createCollection('outboxes'); db.outboxes.createIndex({eventId:1},{unique:true}); db.outboxes.createIndex({status:1, scheduledAt:1}); db.createCollection('eventstore'); db.eventstore.insertOne({eventId:'x'}); db.createCollection('patients'); db.patients.insertOne({nome:'PACIENTE-FICTICIO'}); 1" >/dev/null
ok "usuários: root, crm_app (readWrite), kas_outbox_shadow (papel kasOutboxShadowRead = find em outboxes), kas_bad (readWrite)"

say "T1  o servidor recusa o que a sombra não deve fazer"
shm() { docker exec "$M" mongosh --quiet "mongodb://kas_outbox_shadow:${SHPW}@127.0.0.1:27017/${DB}?authSource=admin&directConnection=true" --eval "$1" 2>&1 || true; }
denied() { grep -qi "not authorized\|Unauthorized"; }
shm "db.outboxes.insertOne({eventId:'x'})" | denied && ok "insert no outboxes: recusado" || bad "insert do usuário da sombra não foi recusado"
shm "db.outboxes.updateOne({}, {\$set:{status:'pending'}})" | denied && ok "update no outboxes (failed -> pending): recusado" || bad "update não recusado"
shm "db.outboxes.deleteOne({})" | denied && ok "delete no outboxes: recusado" || bad "delete não recusado"
shm "db.outboxes.createIndex({x:1})" | denied && ok "createIndex: recusado" || bad "createIndex não recusado"
shm "db.eventstore.findOne()" | denied && ok "leitura do eventstore: recusada (papel separado da sombra do reaper)" || bad "usuário do outbox lê o eventstore"
shm "db.patients.findOne()" | denied && ok "leitura de patients: recusada" || bad "usuário do outbox lê patients"
shm "db.outboxes.countDocuments({})" | grep -qE '^[0-9]+$' && ok "leitura do outboxes: permitida" || bad "usuário da sombra não lê o outboxes"

say "T2  a sombra recusa iniciar sem prova de só leitura"
KAS_OUTBOX_MONGO_URI="$BAD_URI" "$WORK/kas-outbox-recovery" -mode shadow -db "$DB" -require-read-only on -once >"$WORK/t2-bad.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 2 ] && grep -q shadow_refused_not_read_only "$WORK/t2-bad.log" && ok "usuário com readWrite: recusa (código 2), nenhum ciclo" || { bad "aceitou usuário que escreve (rc=$rc)"; cat "$WORK/t2-bad.log"; }
KAS_OUTBOX_MONGO_URI="$NOAUTH_URI" "$WORK/kas-outbox-recovery" -mode shadow -db "$DB" -require-read-only on -once >"$WORK/t2-noauth.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 2 ] && grep -q "no authenticated user" "$WORK/t2-noauth.log" && ok "sem usuário autenticado: recusa" || { bad "aceitou conexão sem usuário (rc=$rc)"; cat "$WORK/t2-noauth.log"; }
KAS_OUTBOX_MONGO_URI="$SHADOW_URI" "$WORK/kas-outbox-recovery" -mode executor -db "$DB" -executor-confirm-db "$DB" -require-read-only on -once >"$WORK/t2-exec.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 2 ] && grep -q "shadow mode only" "$WORK/t2-exec.log" && ok "modo executor com a prova de só leitura: recusado antes de conectar" || bad "executor aceito com -require-read-only (rc=$rc)"
KAS_OUTBOX_MONGO_URI="$SHADOW_URI" "$WORK/kas-outbox-recovery" -mode shadow -db "$DB" -require-read-only on -once -report "$WORK/t2-ok.jsonl" >"$WORK/t2-ok.log" 2>&1 && rc=0 || rc=$?
[ "$rc" = 0 ] && grep -q read_only_confirmed "$WORK/t2-ok.log" && grep -q '"actions":\["find"\]' "$WORK/t2-ok.jsonl" && ok "usuário da sombra: read_only_confirmed, ações = [find]" || { bad "usuário da sombra não confirmou (rc=$rc)"; cat "$WORK/t2-ok.log"; }

say "T3  só a sombra rodando (dados fictícios): banco intacto e contagem exata"
seed_failed
H0=$(madmin "db.getSiblingDB('${DB}').runCommand({dbHash:1}).md5")
T0=$(madmin 'const t=db.hello().operationTime; JSON.stringify({t:t.t,i:t.i})')
shadow_start t3 "$SHADOW_URI"; S3=$SH_PID
sleep 7
kill -TERM "$S3"; wait "$S3" 2>/dev/null || true
H1=$(madmin "db.getSiblingDB('${DB}').runCommand({dbHash:1}).md5")
OPS=$(madmin "const t=${T0}; db.getSiblingDB('local').oplog.rs.countDocuments({ts:{\$gt: Timestamp(t.t, t.i)}, ns: /^${DB}\\./})")
[ "$H0" = "$H1" ] && ok "dbHash igual antes e depois" || bad "dbHash mudou com só a sombra rodando"
[ "$OPS" = 0 ] && ok "oplog: 0 escritas no banco durante a sombra" || bad "oplog com $OPS escritas"
grep -q 'PACIENTE-FICTICIO' "$WORK/t3.jsonl" && bad "payload apareceu no relatório" || ok "relatório sem payload"
set +e; "$WORK/kas-outbox-shadow-summary" -min-days 0 -json-out "$WORK/t3-summary.json" "$WORK/t3.jsonl" >"$WORK/t3-summary.md"; SRC=$?; set -e
echo "--- resumo do TESTE FICTÍCIO (não são números de produção) ---"; cat "$WORK/t3-summary.md"; echo "---"
[ "$SRC" = 0 ] && ok "resumo: todos os critérios passam (período parcial)" || bad "resumo com código $SRC"
J() { node -e "const r=require('$WORK/t3-summary.json'); const v=($1); process.stdout.write(String(v))"; }
[ "$(J 'r.failed_entries')" = 27 ] && ok "27 entradas failed fictícias vistas" || bad "entradas: $(J 'r.failed_entries') (esperado 27)"
[ "$(J 'r.by_action.recover')" = 18 ] && ok "recuperaria 18 (12 APPOINTMENT_CREATED + 6 PATIENT_UPDATED)" || bad "recover: $(J 'r.by_action.recover')"
[ "$(J 'r.by_reason.attempts_exhausted')" = 4 ] && [ "$(J 'r.by_reason.permanent_error')" = 3 ] && ok "desistiria de 7 (4 por tentativas, 3 UNKNOWN_EVENT_TYPE)" || bad "motivos: $(J 'JSON.stringify(r.by_reason)')"
[ "$(J 'r.by_reason.backoff_not_elapsed')" = 2 ] && ok "aguardaria 2 (backoff do dispatcher ainda não venceu)" || bad "backoff: $(J 'r.by_reason.backoff_not_elapsed')"
[ "$(J 'r.would_recover_external_send')" = 12 ] && [ "$(J 'r.would_recover_by_queue.notification')" = 12 ] && ok "12 com destino que pode enviar a uma pessoa (fila notification)" || bad "envio externo: $(J 'r.would_recover_external_send')"
[ "$(J 'r.would_recover_by_queue["patient-projection"]')" = 18 ] && ok "patient-projection: 18" || bad "patient-projection: $(J 'r.would_recover_by_queue["patient-projection"]')"
[ "$(J 'r.would_recover_by_age["1 a 7 dias"]')" = 12 ] && [ "$(J 'r.would_recover_by_age["até 1 h"]')" = 6 ] && ok "idade: 12 entre 1 e 7 dias, 6 até 1 h" || bad "idade: $(J 'JSON.stringify(r.would_recover_by_age)')"
[ "$(J 'r.would_recover_by_attempts["2"]')" = 12 ] && [ "$(J 'r.would_recover_by_attempts["1"]')" = 6 ] && ok "tentativas: 12 com 2, 6 com 1" || bad "tentativas: $(J 'JSON.stringify(r.would_recover_by_attempts)')"
grep -q 'APPOINTMENT_CONFIRMED | 3 | 0 | 3 | 0 | sem rota (UNKNOWN_EVENT_TYPE)' "$WORK/t3-summary.md" && ok "tipo sem rota aparece como tal" || bad "linha de APPOINTMENT_CONFIRMED não bate"

say "T4  dispatcher REAL do CRM ao lado: só as pending são publicadas"
FH0=$(failed_hash)
shadow_start t4 "$SHADOW_URI"; S4=$SH_PID
dispatcher_start
seed_pending p 40
wait_published p 40 40 && ok "dispatcher do CRM publicou 40/40 pending com a sombra ao lado" || bad "publicados: $(published p)/40"
sleep 3
kill -TERM "$S4"; wait "$S4" 2>/dev/null || true
[ "$(failed_hash)" = "$FH0" ] && ok "as 27 entradas failed estão byte a byte iguais" || bad "alguma entrada failed mudou"
[ "$(redis_jobs f-)" = 0 ] && ok "nenhum job no Redis para entrada failed (nada republicado)" || bad "jobs de failed no Redis: $(redis_jobs f-)"
[ "$(redis_jobs p-)" = 40 ] && ok "40 jobs no Redis, todos das pending (fila patient-projection)" || bad "jobs das pending: $(redis_jobs p-)"
[ "$(mdb 'db.outboxes.countDocuments({kasRecovery: {$exists: true}})')" = 0 ] && ok "nenhuma anotação kasRecovery (a sombra não marca nada)" || bad "há anotação kasRecovery"
[ "$(mdb "db.outboxes.countDocuments({eventId: /^f-/, status: 'failed'})")" = 27 ] && ok "27 failed continuam failed" || bad "status das failed mudou"

say "T5  sombra travada e morta não afeta o dispatcher"
shadow_start t5a "$SHADOW_URI"; S5A=$SH_PID
shadow_start t5b "$SHADOW_URI"; S5B=$SH_PID
sleep 3
kill -STOP "$S5A"; kill -9 "$S5B"
seed_pending q 30
wait_published q 30 40 && ok "dispatcher publicou 30/30 com uma sombra travada (SIGSTOP) e outra morta (kill -9)" || bad "publicados: $(published q)/30"
kill -0 "$DISP" 2>/dev/null && ok "processo do dispatcher seguiu de pé" || bad "dispatcher caiu"
kill -CONT "$S5A"; kill -9 "$S5A" 2>/dev/null || true

say "T6  banco pausado: a sombra registra erro e volta sozinha"
shadow_start t6 "$SHADOW_URI"; S6=$SH_PID
sleep 3; docker pause "$M" >/dev/null; sleep 7; docker unpause "$M" >/dev/null; sleep 8
kill -TERM "$S6"; wait "$S6" 2>/dev/null || true
ERRS=$(grep -c '"error":"' "$WORK/t6.jsonl" || true)
LASTOK=$(awk '/"kind":"cycle"/ { if (/"error":"/) e=1; else if (e) n++ } END { print n+0 }' "$WORK/t6.jsonl")
[ "$ERRS" -ge 1 ] && ok "ciclos com erro registrados durante a pausa: $ERRS" || bad "pausa não apareceu no relatório"
[ "$LASTOK" -ge 1 ] && ok "$LASTOK ciclo(s) sem erro depois da volta do banco" || bad "sombra não voltou depois da pausa"
grep -q 'context canceled' "$WORK/t6.jsonl" && bad "SIGTERM virou ciclo com erro" || ok "parada com SIGTERM não vira ciclo com erro"
kill -TERM "$DISP" 2>/dev/null || true

echo
[ "$FAILS" = 0 ] && echo "TUDO OK" || { echo "FALHAS: $FAILS (logs em $WORK)"; exit 1; }
