#!/usr/bin/env bash
# Ambiente integrado CRM + KAS, isolado e descartável (itens 7 e 9 da matriz).
#
# Sobe MongoDB 7 (replica set de 1 nó) e Redis 7 em Docker, só em loopback,
# com dados fictícios. Roda o CÓDIGO REAL do CRM (reaper do Event Store,
# posse de execução de eventos, OutboxDispatcher, alertService) ao lado dos
# binários da KAS (kas-event-reaper, kas-outbox-recovery e a central de
# alertas embutida neles). Provedores simulados: canal de alertas (webhook
# local) e nenhuma fila com consumidor real. Nada lê .env, nada fala com
# Atlas, Render, WhatsApp ou e-mail.
#
#   deploy/integrado/run.sh            # tudo: cortes (item 7) + reversão Node→Go→Node (item 9)
#   KEEP=1 deploy/integrado/run.sh     # mantém contêineres e logs para inspeção
#   CRM_DIR=/caminho/fono-inova-crm-back deploy/integrado/run.sh
#
# Requisitos: docker, go 1.25+, node 20+, e o repositório do CRM com
# `npm ci` feito (padrão: pasta irmã ../fono-inova-crm-back).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
CRM_DIR=${CRM_DIR:-$(cd "$ROOT/.." && pwd)/fono-inova-crm-back}
MPORT=${MPORT:-27317}
RPORT=${RPORT:-6579}
APORT=${APORT:-9599}
M=kas-int-mongo
R=kas-int-redis
DB=isolado_crm
WORK=${WORK:-$(mktemp -d)}
PIDS=()
FAILS=0

say() { printf '\n== %s\n' "$*"; }
ok() { printf 'OK: %s\n' "$*"; }
bad() { printf 'FALHOU: %s\n' "$*"; FAILS=$((FAILS + 1)); }
mongo() { docker exec "$M" mongosh --quiet "$DB" --eval "$1"; }
cleanup() {
  local rc=$?
  set +e
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill -CONT "$p" 2>/dev/null; kill -9 "$p" 2>/dev/null || true; done
  docker unpause "$M" "$R" >/dev/null 2>&1 || true
  if [ "${KEEP:-0}" = 1 ]; then echo "contêineres e logs mantidos em $WORK"; else docker rm -f "$M" "$R" >/dev/null 2>&1; fi
  exit "$rc"
}
trap cleanup EXIT

[ -d "$CRM_DIR/node_modules" ] || { echo "CRM não encontrado em $CRM_DIR (rode npm ci lá ou defina CRM_DIR)"; exit 2; }
[ -f "$CRM_DIR/scripts/isolado/crmReaper.mjs" ] || { echo "o CRM em $CRM_DIR não tem scripts/isolado (branch com o ambiente integrado)"; exit 2; }

CRM_URI="mongodb://127.0.0.1:${MPORT}/${DB}?directConnection=true"
KAS_URI="mongodb://127.0.0.1:${MPORT}/?directConnection=true"
export KAS_REAPER_MONGO_URI="$KAS_URI" KAS_OUTBOX_MONGO_URI="$KAS_URI"

# Processo do CRM com ambiente limpo: só o que o ambiente isolado define.
# exec: o PID em $! é o do próprio node (kill -9 atinge o processo do CRM).
# Só chamar em segundo plano (&) ou dentro de $( ).
crm() {
  local script=$1; shift
  exec env -i PATH="$PATH" HOME="$HOME" NODE_ENV=isolado TZ=America/Sao_Paulo \
    CRM_ISOLADO_MONGO_URI="$CRM_URI" REDIS_URL="redis://127.0.0.1:${RPORT}" \
    ALERT_WEBHOOK_URL="http://127.0.0.1:${APORT}/alert" SLACK_WEBHOOK_URL= ALERT_EMAIL_ENABLED=false \
    WORKER_SIM_TOKENS="$WORK/worker-tokens.json" \
    "${CRM_ENV[@]}" node "$CRM_DIR/scripts/isolado/$script" "$@"
}
CRM_ENV=()

reaper_node_start() { # $1 = owner (node|kas), $2 = log
  CRM_ENV=(EVENT_REAPER_OWNER="$1" EVENT_REAPER_SHARED_LEASE=on EVENT_REAPER_LEASE_TTL_MS=6000
           EVENT_REAPER_INTERVAL_MS=1000 EVENT_REAPER_WARMUP_MS=200 EVENT_LEASE_MS=3000)
  crm crmReaper.mjs >>"$2" 2>&1 & NODE_PID=$!
  CRM_ENV=()
  PIDS+=("$NODE_PID")
}
reaper_go_start() { # $1 = holder, $2 = report
  "$WORK/kas-event-reaper" -mode executor -protocol v2 -db "$DB" -executor-confirm-db "$DB" \
    -interval 1s -warmup 0s -lease-ttl 6s -cycle-timeout 3s -batch 50 -holder "$1" -report "$2" \
    >>"$WORK/$1.log" 2>&1 & GO_PID=$!
  PIDS+=("$GO_PID")
}
seed_events() { # $1 = prefixo, $2 = quantidade (pending, prontos para o worker simulado)
  mongo "
    const docs = [];
    for (let i = 0; i < $2; i++) docs.push({eventId: '$1-' + i, eventType: 'APPOINTMENT_CREATED', aggregateType: 'appointment',
      aggregateId: 'agg-' + i, payload: {nome: 'PACIENTE-FICTICIO'}, status: 'pending', attempts: 0, createdAt: new Date()});
    db.eventstore.insertMany(docs).insertedIds.length" >/dev/null
}
abandon() { # $1 = prefixo, $2 = n: worker real toma posse e morre com kill -9
  CRM_ENV=(EVENT_LEASE_MS=2000)
  crm crmWorkerSim.mjs claim "$1" "$2" 2000 >>"$WORK/worker.log" 2>&1 & local w=$!
  CRM_ENV=()
  for _ in $(seq 1 100); do grep -q "worker_sim_claimed" "$WORK/worker.log" 2>/dev/null && [ "$(grep -c worker_sim_claimed "$WORK/worker.log")" -ge "${ABANDONS:-1}" ] && break; sleep 0.1; done
  kill -9 "$w" 2>/dev/null || true
  ABANDONS=$(( ${ABANDONS:-1} + 1 ))
}
processing_of() { mongo "db.eventstore.countDocuments({eventId: /^$1-/, status: 'processing'})"; }
recovered_once() { mongo "db.eventstore.countDocuments({eventId: /^$1-/, status: 'pending', recoveryCount: 1})"; }
wait_recovered() { # $1 prefixo, $2 n, $3 timeout s
  for _ in $(seq 1 "$3"); do [ "$(recovered_once "$1")" = "$2" ] && return 0; sleep 1; done; return 1
}
lease_holder() { mongo 'const d = db.kas_event_reaper_lease.findOne(); d && d.expiresAt > new Date() ? d.holder : "-"'; }
analyze() { node "$CRM_DIR/scripts/isolado/analyze.mjs" "$WORK/observer.jsonl" "${1:-}"; }
obs_ok() { # $1 = JSON do analyze, $2 = mínimo de recuperações esperado
  local n; n=$(echo "$1" | sed -n 's/.*"recoveries":\([0-9]*\).*/\1/p')
  [ "${n:-0}" -ge "$2" ] && echo "$1" | grep -q '"unfenced":0' && echo "$1" | grep -q '"dupSameOccurrence":0'
}
cluster_now() { mongo 'const t = db.hello().operationTime; t.t + "." + t.i'; }

# ---------------------------------------------------------------------------
say "build dos binários da KAS"
(cd "$ROOT" && go build -o "$WORK/kas-event-reaper" ./cmd/kas-event-reaper && go build -o "$WORK/kas-outbox-recovery" ./cmd/kas-outbox-recovery)

say "MongoDB 7 (replica set) em 127.0.0.1:${MPORT} e Redis 7 em 127.0.0.1:${RPORT}, descartáveis"
docker rm -f "$M" "$R" >/dev/null 2>&1 || true
docker run -d --name "$M" -p "127.0.0.1:${MPORT}:27017" mongo:7 --replSet rs0 --bind_ip_all >/dev/null
docker run -d --name "$R" -p "127.0.0.1:${RPORT}:6379" redis:7-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$M" mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$M" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]}).ok' >/dev/null
for _ in $(seq 1 30); do [ "$(docker exec "$M" mongosh --quiet --eval 'db.hello().isWritablePrimary')" = true ] && break; sleep 1; done
for _ in $(seq 1 30); do docker exec "$R" redis-cli ping >/dev/null 2>&1 && break; sleep 1; done
mongo 'db.createCollection("eventstore"); db.eventstore.createIndex({eventId:1},{unique:true}); db.createCollection("kas_event_reaper_lease"); db.createCollection("outboxes"); db.outboxes.createIndex({eventId:1},{unique:true}); 1' >/dev/null

say "canal de alertas simulado e observador do banco (change stream)"
node "$HERE/alert-sink.mjs" "$APORT" "$WORK/alerts.jsonl" >"$WORK/alert-sink.log" 2>&1 & SINK_PID=$!; PIDS+=("$SINK_PID")
crm observer.mjs "$WORK/observer.jsonl" >"$WORK/observer.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do grep -q observer_ready "$WORK/observer.log" 2>/dev/null && break; sleep 0.2; done
grep -q observer_ready "$WORK/observer.log" && ok "observador pronto" || { bad "observador não subiu"; cat "$WORK/observer.log"; exit 1; }

# ===========================================================================
say "ITEM 7.1  Reaper: worker real do CRM morre com posse; Node (dono) e KAS (executor) disputando"
seed_events a 150
abandon a 150
echo "eventos abandonados em processing: $(processing_of a)"
T0=$(cluster_now)
reaper_node_start node "$WORK/node-1.log"; NODE1=$NODE_PID
reaper_go_start kas-A "$WORK/kas-A.jsonl"; GOA=$GO_PID
wait_recovered a 150 60 && ok "150/150 recuperados exatamente uma vez (recoveryCount=1)" || bad "recuperação incompleta: $(recovered_once a)/150"
R1=$(analyze "$T0"); echo "observador: $R1"
obs_ok "$R1" 150 && ok "observador: 150 recuperações, todas gravadas junto com a confirmação da posse, nenhuma repetida" || bad "observador do lote a" 
echo "dono da posse agora: $(lease_holder)"

say "ITEM 7.2  Concorrência: segundo executor KAS entra; só um grava por vez"
reaper_go_start kas-B "$WORK/kas-B.jsonl"; GOB=$GO_PID
seed_events b 120; abandon b 120
T1=$(cluster_now)
wait_recovered b 120 60 && ok "120/120 recuperados uma vez com três candidatos a dono (Node, kas-A, kas-B)" || bad "lote b: $(recovered_once b)/120"
R2=$(analyze "$T1"); echo "observador: $R2"
obs_ok "$R2" 120 && ok "sem escrita fora da posse e sem duplicidade" || bad "lote b com escrita fora da posse ou duplicada"
nl=$(cat "$WORK"/kas-A.jsonl "$WORK"/kas-B.jsonl 2>/dev/null | grep -c '"not_leader"' || true)
echo "ciclos da KAS pulados por não serem donos: $nl"

say "ITEM 7.3  Indisponibilidade: MongoDB pausado 8 s com eventos presos; depois volta"
T2=$(cluster_now)
seed_events c 100; abandon c 100
docker pause "$M" >/dev/null; sleep 8; docker unpause "$M" >/dev/null
wait_recovered c 100 60 && ok "100/100 recuperados depois da volta do banco" || bad "lote c: $(recovered_once c)/100"
for p in "$NODE1" "$GOA" "$GOB"; do kill -0 "$p" 2>/dev/null || bad "processo $p morreu com a indisponibilidade"; done
ok "Node e os dois executores KAS continuam de pé depois da pausa"

say "ITEM 7.4  Reinício: o dono atual cai com kill -9 (sem devolver a posse)"
seed_events d 80; abandon d 80
h=$(lease_holder); echo "dono antes da queda: $h"
case "$h" in
  kas-A) kill -9 "$GOA" ;; kas-B) kill -9 "$GOB" ;; crm-node*) kill -9 "$NODE1" ;; *) echo "sem dono vivo neste instante" ;;
esac
wait_recovered d 80 60 && ok "80/80 recuperados por outro dono depois do prazo da posse vencer" || bad "lote d: $(recovered_once d)/80"
echo "novo dono: $(lease_holder)"
R3=$(analyze "$T2"); echo "observador (7.3 + 7.4): $R3"
obs_ok "$R3" 180 && ok "queda do dono não gerou escrita fora da posse nem duplicidade" || bad "falha em 7.3/7.4"
for p in "$NODE1" "$GOA" "$GOB"; do kill -TERM "$p" 2>/dev/null || true; done; sleep 2

say "ITEM 7.5  Alertas: dead_letter avisa no canal; canal fora do ar não trava o reaper"
# Evento com 2 recuperações anteriores: a 3ª leva a dead_letter + alerta crítico.
mongo "db.eventstore.insertOne({eventId:'dl-0', eventType:'APPOINTMENT_CREATED', aggregateType:'appointment', aggregateId:'x', payload:{}, status:'processing', attempts:1, recoveryCount:2, processingToken:'morto', leaseExpiresAt:new Date(Date.now()-60000), createdAt:new Date()}).acknowledged" >/dev/null
reaper_node_start node "$WORK/node-2.log"; NODE2=$NODE_PID
for _ in $(seq 1 30); do [ "$(mongo "db.eventstore.findOne({eventId:'dl-0'}).status")" = dead_letter ] && break; sleep 1; done
sleep 1
grep -q '"type":"event_recovery_limit"' "$WORK/alerts.jsonl" && ok "alerta crítico event_recovery_limit chegou ao canal simulado" || bad "alerta não chegou ao canal"
kill "$SINK_PID" 2>/dev/null || true; sleep 0.5
mongo "db.eventstore.insertOne({eventId:'dl-1', eventType:'SESSION_COMPLETED', aggregateType:'session', aggregateId:'y', payload:{}, status:'processing', attempts:1, recoveryCount:2, processingToken:'morto', leaseExpiresAt:new Date(Date.now()-60000), createdAt:new Date()}).acknowledged" >/dev/null
seed_events e 30; abandon e 30
wait_recovered e 30 40 && ok "com o canal fora do ar o reaper seguiu: 30/30 recuperados" || bad "reaper travou sem canal de alertas"
[ "$(mongo "db.eventstore.findOne({eventId:'dl-1'}).status")" = dead_letter ] && ok "dead_letter gravado mesmo sem canal (aviso perdido fica no log: limite conhecido)" || bad "dl-1 não foi para dead_letter"
kill -TERM "$NODE2" 2>/dev/null || true; sleep 2
node "$HERE/alert-sink.mjs" "$APORT" "$WORK/alerts.jsonl" >>"$WORK/alert-sink.log" 2>&1 & SINK_PID=$!; PIDS+=("$SINK_PID")

say "ITEM 7.6  Worker que perdeu a posse do EVENTO não conclui (eventOwnership real)"
out=$(CRM_ENV=(); crm crmWorkerSim.mjs late-complete x 0 2>&1 | tail -1); echo "$out"
echo "$out" | grep -q '"ok":0' && ok "conclusão tardia com token antigo recusada em todos os eventos" || bad "worker sem posse conseguiu concluir"

# ===========================================================================
say "ITEM 7.7  Outbox: dispatcher real do CRM + recuperação de failed da KAS + Redis fora do ar"
mongo "
  const now = Date.now(), docs = [];
  for (let i = 0; i < 60; i++) docs.push({eventId: 'ob-' + i, correlationId: 'c-' + i, eventType: 'APPOINTMENT_CREATED', aggregateType: 'appointment',
    aggregateId: 'agg-' + i, payload: {nome: 'PACIENTE-FICTICIO'}, status: 'failed', attempts: (i % 3) + 1, lastError: 'Connection is closed.',
    scheduledAt: new Date(now - 60000), createdAt: new Date(now - 3600000)});
  for (let i = 0; i < 5; i++) docs.push({eventId: 'ob-unk-' + i, correlationId: 'u-' + i, eventType: 'EVENTO_FICTICIO_SEM_FILA', aggregateType: 'x',
    aggregateId: 'x', payload: {}, status: 'pending', attempts: 0, scheduledAt: new Date(now - 60000), createdAt: new Date(now)});
  db.outboxes.insertMany(docs).insertedIds.length" >/dev/null
crm crmDispatcher.mjs >"$WORK/dispatcher.log" 2>&1 & DISP=$!; PIDS+=("$DISP")
"$WORK/kas-outbox-recovery" -mode executor -db "$DB" -executor-confirm-db "$DB" -interval 1s >"$WORK/outbox-1.log" 2>&1 & OB1=$!; PIDS+=("$OB1")
"$WORK/kas-outbox-recovery" -mode executor -db "$DB" -executor-confirm-db "$DB" -interval 1s >"$WORK/outbox-2.log" 2>&1 & OB2=$!; PIDS+=("$OB2")
sleep 3
echo "pausando o Redis 6 s com recuperação e dispatcher ligados"; docker pause "$R" >/dev/null; sleep 6; docker unpause "$R" >/dev/null
kill -9 "$OB1"; echo "recuperador 1 morto com kill -9"
for _ in $(seq 1 60); do [ "$(mongo "db.outboxes.countDocuments({eventId:/^ob-[0-9]/, status:'published'})")" = 60 ] && break; sleep 1; done
pub=$(mongo "db.outboxes.countDocuments({eventId:/^ob-[0-9]/, status:'published'})")
[ "$pub" = 60 ] && ok "60/60 falhas recuperadas pela KAS e publicadas pelo dispatcher do CRM" || bad "outbox publicados: $pub/60"
jobs=$(docker exec "$R" redis-cli --scan --pattern 'bull:*:ob-[0-9]*__*' | grep -vc ':lock' || true)
echo "jobs no Redis com jobId eventId__fila: $jobs (esperado 180 = 60 × 3 filas)"
[ "$jobs" = 180 ] && ok "um job por evento e fila, sem duplicata" || bad "jobs no Redis: $jobs"
gu=$(mongo "db.outboxes.countDocuments({eventId:/^ob-unk-/, status:'failed', 'kasRecovery.exhaustedAt': {\$exists: true}})")
[ "$gu" = 5 ] && ok "UNKNOWN_EVENT_TYPE: 5/5 marcados como desistidos (não voltam a pending)" || bad "desistidos: $gu/5"
grep -q 'outbox_recovery_exhausted\|exhausted' "$WORK/outbox-2.log" && ok "alerta de desistência no log estruturado da KAS (canal real é decisão de ativação)" || bad "sem alerta de desistência no log da KAS"
kill -TERM "$DISP" "$OB2" 2>/dev/null || true

# ===========================================================================
say "ITEM 9  Reversão Node → Go → Node"
TR=$(cluster_now)
echo "-- fase 1: Node é o dono (EVENT_REAPER_OWNER=node, posse compartilhada ligada)"
reaper_node_start node "$WORK/rev-node-1.log"; RN1=$NODE_PID
seed_events r1 50; abandon r1 50
wait_recovered r1 50 30 && ok "fase 1: Node recuperou 50/50" || bad "fase 1: $(recovered_once r1)/50"
echo "-- fase 2a: executor Go sobe ANTES de o Node sair (erro de operação simulado)"
reaper_go_start kas-rev "$WORK/kas-rev.jsonl"; GREV=$GO_PID
seed_events r2 40; abandon r2 40
wait_recovered r2 40 30 && ok "r2 recuperados 40/40 com Node e Go de pé" || bad "r2: $(recovered_once r2)/40"
R4=$(analyze "$TR"); echo "observador: $R4"
echo "$R4" | grep -q '"kas-rev"' && bad "Go gravou enquanto o Node tinha a posse" || ok "Go de pé não gravou nada: o Node segurava a posse"
echo "-- fase 2b: Node sai (EVENT_REAPER_OWNER=kas + reinício, SIGTERM devolve a posse)"
kill -TERM "$RN1"; wait "$RN1" 2>/dev/null || true
reaper_node_start kas "$WORK/rev-node-2.log"; RN2=$NODE_PID
sleep 2
grep -q 'crm_reaper_not_started' "$WORK/rev-node-2.log" && ok "Node com EVENT_REAPER_OWNER=kas não iniciou o reaper" || bad "Node com owner=kas iniciou o reaper"
seed_events r3 40; abandon r3 40
TG=$(cluster_now)
wait_recovered r3 40 30 && ok "fase 2: Go recuperou 40/40" || bad "fase 2: $(recovered_once r3)/40"
echo "dono: $(lease_holder)"
echo "-- fase 3a: o executor Go TRAVA com a posse (SIGSTOP) e o Node volta como dono"
seed_events r4 30
kill -STOP "$GREV"; echo "Go congelado com a posse"
kill -TERM "$RN2" 2>/dev/null || true; wait "$RN2" 2>/dev/null || true
reaper_node_start node "$WORK/rev-node-3.log"; RN3=$NODE_PID
abandon r4 30
wait_recovered r4 30 40 && ok "fase 3: Node retomou depois do prazo da posse do Go vencer e recuperou 30/30" || bad "fase 3: $(recovered_once r4)/30"
echo "dono: $(lease_holder)"
echo "-- fase 3b: o Go antigo acorda (SIGCONT) com eventos novos presos; não pode gravar"
seed_events r5 30
TS=$(cluster_now)
kill -CONT "$GREV"
abandon r5 30
wait_recovered r5 30 40 && ok "r5 recuperados 30/30" || bad "r5: $(recovered_once r5)/30"
sleep 3
R5=$(analyze "$TS"); echo "observador depois do SIGCONT: $R5"
echo "$R5" | grep -q '"kas-rev"' && bad "executor Go sem posse gravou depois de acordar" || ok "executor Go que perdeu a posse não gravou nada depois de acordar"
grep -c '"lease_lost"\|"not_leader"' "$WORK/kas-rev.jsonl" | xargs -I{} echo "ciclos do Go recusados (not_leader/lease_lost): {}"
kill -TERM "$GREV" 2>/dev/null || true
echo "-- fase 3c: só o Node segue; nada ficou preso"
left=$(mongo "db.eventstore.countDocuments({status:'processing', leaseExpiresAt: {\$lte: new Date(Date.now() - 10000)}})")
[ "$left" = 0 ] && ok "nenhum evento com posse vencida esquecido em processing" || bad "$left evento(s) presos"
RT=$(analyze "$TR"); echo "observador, reversão inteira: $RT"
obs_ok "$RT" 190 && ok "reversão inteira: toda escrita dentro da posse, nenhuma duplicada" || bad "reversão com escrita fora da posse ou duplicada"
kill -TERM "$RN3" 2>/dev/null || true; sleep 1

say "resumo"
echo "logs e relatórios: $WORK"
if [ "$FAILS" = 0 ]; then echo "TUDO OK"; else echo "$FAILS verificação(ões) falharam"; exit 1; fi
