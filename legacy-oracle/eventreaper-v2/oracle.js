// Behavioural oracle for protocol v2: runs the UNMODIFIED reaper from the CRM
// branch feat/eventstore-posse against a disposable MongoDB and prints one JSON
// line per cycle on stdout. The module already exports reapStuckEvents, so no
// shim is needed; the clock and the alert sink are injected through its
// parameters ({ now, alert }), which is the module's own public contract.
//
// The vendored files are verified against LEGACY_SHA256SUMS before use.
//
// Environment:
//   ORACLE_MONGO_URI  disposable MongoDB URI (required; must be loopback)
//   ORACLE_DB         database name (required)
//   ORACLE_NOW_MS     the reaper's clock, epoch milliseconds (required)
//   ORACLE_UNTIL_EMPTY=1  repeat until a cycle finds nothing
//   EVENT_LEGACY_FENCE_AT  passed through to the CRM code (legacy fence)
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const legacy = join(here, 'legacy');

function fail(msg) {
  process.stderr.write(msg + '\n');
  process.exit(2);
}

const uri = process.env.ORACLE_MONGO_URI;
const dbName = process.env.ORACLE_DB;
const nowMs = Number(process.env.ORACLE_NOW_MS);
if (!uri || !dbName || !Number.isFinite(nowMs)) fail('ORACLE_MONGO_URI, ORACLE_DB and ORACLE_NOW_MS are required');
const host = new URL(uri).hostname;
if (!['localhost', '127.0.0.1', '::1', '[::1]'].includes(host)) fail(`refusing non-loopback MongoDB host ${host}`);

for (const line of readFileSync(join(here, 'LEGACY_SHA256SUMS'), 'utf8').trim().split('\n')) {
  const [sum, file] = line.trim().split(/\s+/);
  const got = createHash('sha256').update(readFileSync(join(legacy, file))).digest('hex');
  if (got !== sum) fail(`legacy file ${file} changed: ${got} != ${sum}`);
}

const realLog = console.log;
console.log = (...a) => process.stderr.write(a.join(' ') + '\n');
console.info = console.warn = console.debug = console.error = console.log;

const mongoose = (await import('mongoose')).default;
await mongoose.connect(uri, { dbName, serverSelectionTimeoutMS: 5000 });
let code = 0;
try {
  const { reapStuckEvents } = await import(pathToFileURL(join(legacy, 'crons', 'eventReaper.cron.js')).href);
  const alerts = [];
  const untilEmpty = process.env.ORACLE_UNTIL_EMPTY === '1';
  for (let i = 0; i < 10000; i++) {
    alerts.length = 0;
    const result = await reapStuckEvents({ now: new Date(nowMs), alert: async (a) => { alerts.push(a.details.eventId); } });
    delete result.details;
    result.alerts = alerts.slice().sort();
    realLog(JSON.stringify(result));
    if (!untilEmpty || result.total === 0) break;
  }
} catch (e) {
  process.stderr.write(String(e?.stack || e) + '\n');
  code = 1;
} finally {
  await mongoose.disconnect();
}
// alertService keeps a timer alive; leave explicitly.
process.exit(code);
