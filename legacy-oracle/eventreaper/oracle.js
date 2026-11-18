// Behavioural oracle: runs the UNMODIFIED legacy reaper function once against a
// disposable MongoDB and prints its return value as JSON on stdout.
//
// The vendored legacy files are verified against LEGACY_SHA256SUMS before use.
// reapStuckEvents is not exported by the legacy module, so the oracle writes a
// sibling copy that only appends an export line; the legacy text is untouched.
//
// Environment:
//   ORACLE_MONGO_URI  disposable MongoDB URI (required; must be loopback)
//   ORACLE_DB         database name (required)
//   ORACLE_NOW_MS     clock for Date.now(), epoch milliseconds (required)
//   ORACLE_UNTIL_EMPTY=1  call reapStuckEvents repeatedly until it finds nothing,
//                     printing one JSON line per call (each call is one legacy cycle)
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
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

const src = readFileSync(join(legacy, 'crons', 'eventReaper.cron.js'), 'utf8');
const shim = join(legacy, 'crons', '.eventReaper.oracle.js');
writeFileSync(shim, src + '\nexport { reapStuckEvents };\n');

// Silence the legacy console noise; stdout carries only the JSON result.
const realLog = console.log;
console.log = (...a) => process.stderr.write(a.join(' ') + '\n');
console.info = console.warn = console.debug = console.log;

const realNow = Date.now;
Date.now = () => nowMs;

const mongoose = (await import('mongoose')).default;
await mongoose.connect(uri, { dbName, serverSelectionTimeoutMS: 5000 });
try {
  const { reapStuckEvents } = await import(pathToFileURL(shim).href);
  const untilEmpty = process.env.ORACLE_UNTIL_EMPTY === '1';
  for (let i = 0; i < 10000; i++) {
    const result = await reapStuckEvents();
    realLog(JSON.stringify(result));
    if (!untilEmpty || result.total === 0) break;
  }
  Date.now = realNow;
} finally {
  await mongoose.disconnect();
}
