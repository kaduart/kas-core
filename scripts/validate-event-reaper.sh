#!/usr/bin/env bash
# End-to-end validation of kas-event-reaper against a DISPOSABLE MongoDB in
# Docker. Fictitious data only. Never point this at a real database: it
# starts its own container and removes it at the end.
#
#   scripts/validate-event-reaper.sh            # full run
#   KEEP=1 scripts/validate-event-reaper.sh     # keep the container for inspection
set -euo pipefail

NAME=${NAME:-kas-reaper-validate}
PORT=${PORT:-27117}
DB=reaper_validacao
URI="mongodb://127.0.0.1:${PORT}/?directConnection=true"
WORK=$(mktemp -d)
BIN="$WORK/kas-event-reaper"

say() { printf '\n== %s\n' "$*"; }
mongo() { docker exec "$NAME" mongosh --quiet "$DB" --eval "$1"; }
cleanup() {
  [ -n "${PIDS:-}" ] && kill $PIDS 2>/dev/null || true
  docker unpause "$NAME" >/dev/null 2>&1 || true
  [ "${KEEP:-0}" = 1 ] || docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

say "build"
go build -o "$BIN" ./cmd/kas-event-reaper

say "disposable MongoDB 7 (single-node replica set) on 127.0.0.1:${PORT}"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:27017" mongo:7 --replSet rs0 --bind_ip_all >/dev/null
for _ in $(seq 1 30); do docker exec "$NAME" mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$NAME" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]}).ok' >/dev/null
for _ in $(seq 1 30); do [ "$(docker exec "$NAME" mongosh --quiet --eval 'db.hello().isWritablePrimary')" = true ] && break; sleep 1; done

seed() {
  mongo '
    db.eventstore.drop();
    db.eventstore.createIndex({eventId:1},{unique:true});
    const now = Date.now(), docs = [];
    for (let i = 0; i < 3000; i++) {
      docs.push({eventId: "evt-" + i, eventType: "APPOINTMENT_CREATED", aggregateType: "appointment",
        aggregateId: "agg-" + i, payload: {nome: "PACIENTE-FICTICIO"},
        status: i % 10 === 0 ? "processed" : "processing", attempts: i % 5,
        createdAt: new Date(now - (i % 7 === 0 ? 60000 : 3600000) - i)});
    }
    db.eventstore.insertMany(docs).insertedIds.length'
}
stuck() { mongo 'db.eventstore.countDocuments({status:"processing", createdAt:{$lt:new Date(Date.now()-15*60000)}})'; }

export KAS_REAPER_MONGO_URI="$URI"

say "seed 3000 fictitious events"; seed
before=$(stuck); echo "eligible before: $before"

say "1. shadow mode writes nothing"
h1=$(mongo 'db.runCommand({dbHash:1}).md5')
"$BIN" -mode shadow -db "$DB" -once -report "$WORK/shadow.jsonl" | tail -1
h2=$(mongo 'db.runCommand({dbHash:1}).md5')
[ "$h1" = "$h2" ] && echo "OK: database hash unchanged ($h1)" || { echo "FAIL: shadow changed the database"; exit 1; }
grep -q PACIENTE "$WORK/shadow.jsonl" && { echo "FAIL: payload leaked into the report"; exit 1; } || echo "OK: no payload in the report"

say "2. executor refuses without the database confirmation"
if "$BIN" -mode executor -db "$DB" -once >/dev/null 2>&1; then echo "FAIL"; exit 1; else echo "OK: refused"; fi

say "3. two executors at once, database paused 8 s, then the leader is killed (kill -9)"
common=(-mode executor -require-crm-node-lease=false -db "$DB" -executor-confirm-db "$DB" -interval 1s -warmup 0s -lease-ttl 5s -cycle-timeout 3s -batch 25)
"$BIN" "${common[@]}" -holder exec-A -report "$WORK/a.jsonl" >"$WORK/a.log" 2>&1 & PA=$!
"$BIN" "${common[@]}" -holder exec-B -report "$WORK/b.jsonl" >"$WORK/b.log" 2>&1 & PB=$!
PIDS="$PA $PB"
sleep 3
echo "pausing MongoDB for 8 s"; docker pause "$NAME" >/dev/null; sleep 8; docker unpause "$NAME" >/dev/null; echo "MongoDB back; eligible now: $(stuck)"
sleep 3
leader=$(mongo 'db.kas_event_reaper_lease.findOne().holder')
echo "leader is $leader; kill -9 (crash, lease not released). eligible now: $(stuck)"
if [ "$leader" = exec-A ]; then kill -9 $PA; else kill -9 $PB; fi
for _ in $(seq 1 180); do [ "$(stuck)" = 0 ] && break; sleep 1; done
echo "new leader: $(mongo 'db.kas_event_reaper_lease.findOne().holder')"
kill $PIDS 2>/dev/null || true; wait 2>/dev/null || true; PIDS=""

left=$(stuck)
[ "$left" = 0 ] && echo "OK: no eligible event left" || { echo "FAIL: $left left"; exit 1; }
python3 - "$WORK/a.jsonl" "$WORK/b.jsonl" "$before" <<'PY'
import json, sys, collections
applied = collections.Counter(); errs = 0; skipped = collections.Counter(); leaders = collections.Counter()
for path, who in ((sys.argv[1], "A"), (sys.argv[2], "B")):
    for line in open(path):
        r = json.loads(line)
        if r.get("error"): errs += 1
        if r.get("skipped"): skipped[r["skipped"]] += 1
        for o in r.get("outcomes") or []:
            if o["result"] == "applied": applied[o["event_id"]] += 1; leaders[who] += 1
dup = [k for k, v in applied.items() if v > 1]
print(f"applied={len(applied)} expected={sys.argv[3]} duplicates={len(dup)} cycles_with_db_error={errs} skipped={dict(skipped)} applied_by={dict(leaders)}")
assert not dup, "event applied twice"
assert len(applied) == int(sys.argv[3]), "not every eligible event was applied"
assert errs > 0, "the database pause was not observed"
assert all(leaders.values()), "both executors should have led at some point"
print("OK: every eligible event applied exactly once; pause reported and survived; leadership moved after the crash")
PY

say "4. final states match the legacy rule"
mongo 'printjson({
  pending_from_reaper: db.eventstore.countDocuments({status:"pending", "error.message":"Auto-recovered from stuck processing after 15min"}),
  failed_from_reaper:  db.eventstore.countDocuments({status:"failed",  "error.message":"Auto-recovered from stuck processing after 15min"}),
  wrong_pending: db.eventstore.countDocuments({status:"pending", attempts:{$gte:3}}),
  wrong_failed:  db.eventstore.countDocuments({status:"failed", attempts:{$lt:3}}),
  young_still_processing: db.eventstore.countDocuments({status:"processing"}),
  processed_untouched: db.eventstore.countDocuments({status:"processed", error:{$exists:false}}),
  lease: db.kas_event_reaper_lease.findOne({}, {_id:1, holder:1})
})'
echo; echo "validation finished"
