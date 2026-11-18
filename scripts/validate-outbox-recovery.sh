#!/usr/bin/env bash
# End-to-end validation of kas-outbox-recovery against a DISPOSABLE MongoDB in
# Docker, with fictitious data only. It starts its own container on a loopback
# port and removes it at the end. Scenarios:
#   1. shadow writes nothing and never returns payload (census = direct counts)
#   2. guards: executor refused without confirmation or on a remote host
#   3. normal flow: executor recovers what the policy says, exhausts the rest
#   4. failure + recovery: kill -9 mid-cycle, then rerun converges
#   5. concurrency: three executors at once apply each failure once
#   6. reversal: -unmark removes the annotations; stopping restores CRM behaviour
#   7. Node/Go comparison: the unmodified CRM dispatcher with and without
#      recovery (go test oracle), needs node + npm
#
#   scripts/validate-outbox-recovery.sh
#   KEEP=1 scripts/validate-outbox-recovery.sh   # keep the container
set -euo pipefail
cd "$(dirname "$0")/.."

NAME=${NAME:-kas-outbox-validate}
PORT=${PORT:-27118}
DB=outbox_validacao
URI="mongodb://127.0.0.1:${PORT}/?directConnection=true"
WORK=$(mktemp -d)
BIN="$WORK/kas-outbox-recovery"
PIDS=""

say() { printf '\n== %s\n' "$*"; }
ok() { printf 'OK: %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*"; exit 1; }
mongo() { docker exec "$NAME" mongosh --quiet "$DB" --eval "$1"; }
cleanup() {
  [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
  [ "${KEEP:-0}" = 1 ] || docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

say "build"
go build -o "$BIN" ./cmd/kas-outbox-recovery

say "disposable MongoDB 7 (single-node replica set) on 127.0.0.1:${PORT}"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:27017" mongo:7 --replSet rs0 --bind_ip_all >/dev/null
for _ in $(seq 1 30); do docker exec "$NAME" mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$NAME" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]}).ok' >/dev/null
for _ in $(seq 1 30); do [ "$(docker exec "$NAME" mongosh --quiet --eval 'db.hello().isWritablePrimary')" = true ] && break; sleep 1; done

# 4000 fictitious entries shaped like the CRM's Outbox model, with every state
# the dispatcher leaves: published, pending, failed with backoff elapsed or
# not, failed with UNKNOWN_EVENT_TYPE, failed with attempts at the limit.
seed() {
  mongo '
    db.outboxes.drop();
    db.outboxes.createIndex({eventId:1},{unique:true});
    db.outboxes.createIndex({status:1, scheduledAt:1});
    const now = Date.now(), docs = [];
    for (let i = 0; i < 4000; i++) {
      const k = i % 10;
      const d = {eventId: "evt-" + i, correlationId: "corr-" + i, eventType: "FICTICIO", aggregateType: "ficticio",
        aggregateId: "agg-" + i, payload: {nome: "PACIENTE-FICTICIO"}, createdAt: new Date(now - 3600000), attempts: 0,
        scheduledAt: new Date(now - 60000), status: "failed", lastError: "Connection is closed."};
      if (k === 0) { d.status = "published"; d.publishedAt = new Date(now - 86400000 * 8); delete d.lastError; }
      if (k === 1) { d.status = "pending"; delete d.lastError; }
      if (k === 2) { d.attempts = 2; d.scheduledAt = new Date(now + 3600000); }              // backoff not elapsed
      if (k === 3) { d.attempts = 1; d.lastError = "UNKNOWN_EVENT_TYPE: FICTICIO não está em eventToQueueMap"; }
      if (k === 4) { d.attempts = 5; }                                                       // limit reached
      if (k >= 5) { d.attempts = (i % 4) + 1; }                                              // recoverable (1..4)
      docs.push(d);
    }
    db.outboxes.insertMany(docs).insertedIds.length'
}
count() { mongo "db.outboxes.countDocuments($1)"; }
export KAS_OUTBOX_MONGO_URI="$URI"

say "seed 4000 fictitious outbox entries"; seed

say "1. shadow mode writes nothing and never returns payload"
h1=$(mongo 'db.runCommand({dbHash:1}).md5')
"$BIN" -db "$DB" -once -max-pages 1000 -report "$WORK/shadow.jsonl" >/dev/null
h2=$(mongo 'db.runCommand({dbHash:1}).md5')
[ "$h1" = "$h2" ] && ok "database hash unchanged ($h1)" || die "shadow changed the database"
grep -q PACIENTE "$WORK/shadow.jsonl" && die "payload leaked into the report" || ok "no payload in the report"
failed=$(count '{status:"failed"}')
census=$(python3 -c 'import json,sys; print(json.loads(open(sys.argv[1]).readline())["census"]["byStatus"]["failed"])' "$WORK/shadow.jsonl")
[ "$failed" = "$census" ] && ok "census failed=$census matches the direct count" || die "census $census != direct $failed"
would=$(python3 -c 'import json,sys; c=json.loads(open(sys.argv[1]).readline()); print(len(c["wouldDo"].get("recover",[])), len(c["wouldDo"].get("exhaust",[])))' "$WORK/shadow.jsonl")
echo "shadow says: recover/exhaust = $would (expected 2000 800)"
[ "$would" = "2000 800" ] || die "shadow decision counts"

say "2. guards"
"$BIN" -mode executor -db "$DB" -once >/dev/null 2>&1 && die "executor ran without confirmation" || ok "executor refused without -executor-confirm-db"
KAS_OUTBOX_MONGO_URI="mongodb+srv://exemplo.mongodb.net" "$BIN" -mode executor -db "$DB" -executor-confirm-db "$DB" -once >/dev/null 2>&1 && die "remote executor ran" || ok "executor refused on a non-loopback host"

say "4. failure: executor killed with SIGKILL in the middle of a cycle (slow pages)"
"$BIN" -mode executor -db "$DB" -executor-confirm-db "$DB" -batch 5 -max-pages 10000 -interval 1h >"$WORK/k.log" 2>&1 & PK=$!; PIDS="$PK"
for _ in $(seq 1 100); do [ "$(count '{"kasRecovery.count":{$exists:true}}')" -gt 50 ] && break; sleep 0.1; done
kill -9 $PK; wait $PK 2>/dev/null || true; PIDS=""
partial=$(count '{"kasRecovery":{$exists:true}}'); echo "annotated before the crash: $partial of 2800"
[ "$partial" -lt 2800 ] || echo "(the cycle finished before the kill; convergence is still checked below)"

say "5. concurrency + recovery: three executors at once finish the job"
for h in a b c; do "$BIN" -mode executor -db "$DB" -executor-confirm-db "$DB" -batch 50 -max-pages 1000 -once >"$WORK/$h.log" 2>&1 & PIDS="$PIDS $!"; done
wait; PIDS=""

say "3. normal flow: final state"
rec=$(count '{status:"pending","kasRecovery.count":1}')
exh=$(count '{status:"failed","kasRecovery.exhaustedAt":{$exists:true}}')
twice=$(count '{"kasRecovery.count":{$gt:1}}')
untouched=$(count '{status:"failed",attempts:2,kasRecovery:{$exists:false}}')
echo "recovered=$rec (want 2000) exhausted=$exh (want 800) recovered twice=$twice (want 0) backoff left alone=$untouched (want 400)"
[ "$rec" = 2000 ] && [ "$exh" = 800 ] && [ "$twice" = 0 ] && [ "$untouched" = 400 ] || die "final state"
[ "$(count '{"payload.nome":"PACIENTE-FICTICIO"}')" = 4000 ] && ok "payload untouched in all 4000 entries" || die "payload changed"
grep -h '"outbox entry given up"' "$WORK"/{a,b,c,k}.log | wc -l | xargs echo "alerts logged (one per exhausted entry, 800 expected):"

say "repetition: another cycle does nothing"
"$BIN" -mode executor -db "$DB" -executor-confirm-db "$DB" -max-pages 1000 -once | grep '"msg":"cycle"' | grep -q '"recovered":0,"exhausted":0' && ok "nothing left to do" || die "second cycle acted"

say "6. reversal"
"$BIN" -db "$DB" -executor-confirm-db "$DB" -unmark all | grep -o '"entries":[0-9]*'
[ "$(count '{"kasRecovery.exhaustedAt":{$exists:true}}')" = 0 ] && ok "annotations removed; entries are back to plain failed, as the CRM leaves them" || die "unmark"
echo "Stopping the process is the switch: the CRM dispatcher never selects failed entries, so without the KAS it behaves exactly as today."

say "7. Node/Go comparison with the unmodified CRM dispatcher"
if command -v node >/dev/null && command -v npm >/dev/null; then
  (cd legacy-oracle/outbox && npm install --no-audit --no-fund --silent)
  KAS_TEST_MONGO_URI="$URI" KAS_TEST_ORACLE_OUTBOX_DIR="$PWD/legacy-oracle/outbox" \
    go test -count=1 ./internal/legacy/crmoutbox/ -run Oracle -v 2>&1 | grep -E 'oracle_test.go:[0-9]+: [0-9]+ entries|^(ok|FAIL|---)'
else
  echo "SKIPPED: node/npm not installed"
fi

say "all scenarios passed"
