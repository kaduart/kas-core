#!/usr/bin/env bash
# Validates kas-scheduling-mirror end to end against LOCAL databases, with
# fictitious appointments only: full load, verify, incremental, physical delete
# confirmed by two reconciliations, verify again, and a rerun that changes nothing.
#
#   KAS_MIRROR_MONGO_URI=mongodb://127.0.0.1:27217/?directConnection=true \
#   KAS_MIRROR_PG_URL=postgres://kas:...@127.0.0.1:55432/kas_core_test?sslmode=disable \
#   scripts/validate-agenda-mirror.sh
#
# MONGOSH can point to another mongosh (e.g. "docker exec -i mongo-teste mongosh").
# The Mongo database used is kas_espelho_ficticio; it is dropped and recreated.
set -euo pipefail
: "${KAS_MIRROR_MONGO_URI:?set KAS_MIRROR_MONGO_URI (loopback)}"
: "${KAS_MIRROR_PG_URL:?set KAS_MIRROR_PG_URL (loopback)}"
export KAS_MIRROR_KEY="${KAS_MIRROR_KEY:-chave-ficticia-somente-para-validacao-local-0001}"
MONGOSH="${MONGOSH:-mongosh $KAS_MIRROR_MONGO_URI}"
DB=kas_espelho_ficticio
# A new tenant per run: the mirror of earlier runs is never touched.
TENANT="validacao-$(date +%s)"
BIN="$(mktemp -d)/kas-scheduling-mirror"
go build -o "$BIN" ./cmd/kas-scheduling-mirror
mirror() { "$BIN" -tenant "$TENANT" -mongo-db "$DB" -migrate "$@" 2>/dev/null; }
field() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }

echo "== seed: 200 fictitious appointments (3 professionals, March 2027)"
$MONGOSH --quiet <<JS
db = db.getSiblingDB("$DB"); db.dropDatabase();
const docs = [], pros = [new ObjectId(), new ObjectId(), new ObjectId()];
for (let i = 0; i < 200; i++) {
  const day = 1 + Math.floor(i / 24), slot = Math.floor((i % 24) / 3);
  docs.push({ doctor: pros[i % 3], patient: new ObjectId(), date: new Date(Date.UTC(2027, 2, day, 12)),
    time: String(8 + slot).padStart(2, "0") + ":00", duration: 40,
    operationalStatus: ["scheduled", "confirmed", "pre_agendado", "completed", "canceled"][i % 5],
    updatedAt: new Date(Date.now() - 3600e3), isDeleted: false, notes: "fictício" });
}
const times = ["08:00","09:00","10:00","11:00","12:00","13:00","14:00","15:00","16:00","17:00"];
db.doctors.insertMany(pros.map(id => ({ _id: id, fullName: "Profissional Fictício", email: "x@exemplo.invalid", status: "ativo",
  weeklyAvailability: ["monday","tuesday","wednesday","thursday","friday"].map(day => ({ day, times })) })));
docs.push({ patient: new ObjectId(), date: new Date(Date.UTC(2027, 2, 2, 12)), time: "09:00", updatedAt: new Date() });
db.appointments.insertMany(docs);
JS

echo "== full"; R=$(mirror -mode full); echo "$R"
[ "$(echo "$R" | field "['changed']")" = 200 ] || { echo "FAIL: full load must store 200"; exit 1; }
echo "== verify"; V=$(mirror -mode verify -from 2027-03-01 -to 2027-04-01)
[ "$(echo "$V" | field "['equal']")" = True ] || { echo "FAIL: verify"; echo "$V"; exit 1; }
echo "== full again (idempotent)"; R=$(mirror -mode full)
[ "$(echo "$R" | field "['changed']")" = 0 ] || { echo "FAIL: rerun changed rows"; exit 1; }

echo "== change 5, delete 3 in the source"
$MONGOSH --quiet <<JS
db = db.getSiblingDB("$DB");
const ids = db.appointments.find({doctor: {\$exists: true}}).sort({_id: 1}).limit(8).toArray().map(d => d._id);
db.appointments.updateMany({_id: {\$in: ids.slice(0, 5)}}, {\$set: {time: "18:00", updatedAt: new Date()}});
db.appointments.deleteMany({_id: {\$in: ids.slice(5, 8)}});
JS
R=$(mirror -mode incremental); echo "$R"
[ "$(echo "$R" | field "['changed']")" = 5 ] || { echo "FAIL: incremental"; exit 1; }
R=$(mirror -mode reconcile -from 2027-03-01 -to 2027-04-01 -min-gap 0s)
[ "$(echo "$R" | field "['deleted']")" = 0 ] && [ "$(echo "$R" | field "['delete_candidates']")" = 3 ] || { echo "FAIL: first reconcile must only mark"; echo "$R"; exit 1; }
R=$(mirror -mode reconcile -from 2027-03-01 -to 2027-04-01 -min-gap 0s)
[ "$(echo "$R" | field "['deleted']")" = 3 ] || { echo "FAIL: second reconcile must delete 3"; echo "$R"; exit 1; }
V=$(mirror -mode verify -from 2027-03-01 -to 2027-04-01)
[ "$(echo "$V" | field "['equal']")" = True ] || { echo "FAIL: verify after changes"; echo "$V"; exit 1; }
echo "$V" | field "['source_rows'], ['matching_resource_days']" >/dev/null
echo "== sync availability + compare (fase 3A)"
R=$(mirror -mode sync-availability)
[ "$(echo "$R" | field "['professionals_synced']")" = 3 ] || { echo "FAIL: sync availability"; echo "$R"; exit 1; }
C=$(mirror -mode compare -from 2027-03-01 -to 2027-04-01)
echo "$C" | field "['summary']"
[ "$(echo "$C" | field "['summary']['real_divergence']")" = 0 ] && [ "$(echo "$C" | field "['summary']['import_errors']")" = 0 ] || { echo "FAIL: compare"; exit 1; }
mirror -mode compare -from 2027-03-01 -to 2027-04-01 -format md | head -12
H=$(mirror -mode history)
[ "$(echo "$H" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)) >= 2)')" = True ] || { echo "FAIL: history"; exit 1; }
echo "OK: full 200, idempotent rerun, incremental 5, delete after 2 checks, verify equal, compare without import errors or real divergences, history"
