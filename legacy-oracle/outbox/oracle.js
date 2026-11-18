// Behavioural oracle for kas-outbox-recovery: drives the UNMODIFIED CRM outbox
// dispatcher (publishPendingEvents from fono-inova-crm-back
// infrastructure/outbox/outboxPattern.js) against a disposable MongoDB.
//
// It is a long-lived process that reads one JSON command per line on stdin
// and answers one JSON line on stdout:
//   {"cmd":"dispatch","nowMs":<epoch ms>,"batch":100}  -> publishPendingEvents result
//   {"cmd":"jobs"}                                      -> jobs in the in-memory queue + stats
//
// The vendored files are verified against LEGACY_SHA256SUMS. Two imports of
// the dispatcher are redirected to shims by shims/hooks.js (BullMQ queue and
// the event-to-queue map); the clock is injected as a module-scoped Date (a subclass whose
// no-argument form returns the commanded instant) visible only to the dispatcher.
//
// Environment: ORACLE_MONGO_URI (loopback only), ORACLE_DB.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { register } from 'node:module';
import { createInterface } from 'node:readline';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const legacy = join(here, 'legacy');
const fail = (m) => { process.stderr.write(m + '\n'); process.exit(2); };

const uri = process.env.ORACLE_MONGO_URI;
const dbName = process.env.ORACLE_DB;
if (!uri || !dbName) fail('ORACLE_MONGO_URI and ORACLE_DB are required');
const host = new URL(uri).hostname;
if (!['localhost', '127.0.0.1', '::1', '[::1]'].includes(host)) fail(`refusing non-loopback MongoDB host ${host}`);

for (const line of readFileSync(join(here, 'LEGACY_SHA256SUMS'), 'utf8').trim().split('\n')) {
  const [sum, file] = line.trim().split(/\s+/);
  const got = createHash('sha256').update(readFileSync(join(legacy, file))).digest('hex');
  if (got !== sum) fail(`legacy file ${file} changed: ${got} != ${sum}`);
}

// Injected clock.
const RealDate = Date;
let clockMs = RealDate.now();
class OracleDate extends RealDate {
  constructor(...a) { if (a.length === 0) super(clockMs); else super(...a); }
  static now() { return clockMs; }
}
// Only the dispatcher module sees this class (see shims/hooks.js).
globalThis.__oracleDate = OracleDate;

const out = (o) => process.stdout.write(JSON.stringify(o) + '\n');
console.log = console.info = console.warn = console.debug = console.error = (...a) => process.stderr.write(a.join(' ') + '\n');

register(pathToFileURL(join(here, 'shims', 'hooks.js')).href);
const mongoose = (await import('mongoose')).default;
await mongoose.connect(uri, { dbName, serverSelectionTimeoutMS: 5000, autoIndex: false });
const { publishPendingEvents } = await import(pathToFileURL(join(legacy, 'infrastructure', 'outbox', 'outboxPattern.js')).href);
const queue = await import(pathToFileURL(join(here, 'shims', 'queueConfig.js')).href);

out({ ready: true });
const rl = createInterface({ input: process.stdin });
for await (const line of rl) {
  if (!line.trim()) continue;
  const c = JSON.parse(line);
  try {
    if (c.cmd === 'dispatch') {
      // publishPendingEvents silently returns zeros when the connection is not
      // ready; wait for it so a skipped cycle is never mistaken for an empty one.
      let waited = 0;
      while (mongoose.connection.readyState !== 1) {
        if (waited++ > 200) throw new Error('MongoDB connection not ready');
        await new Promise((r) => setTimeout(r, 50));
      }
      clockMs = c.nowMs;
      const r = await publishPendingEvents(c.batch || 100);
      out({ processed: r.processed, published: r.published, failed: r.failed, waitedForConnection: waited });
    } else if (c.cmd === 'jobs') {
      out({ stats: queue.stats, jobs: [...queue.jobs].map(([id, j]) => [id, j.queue, j.eventId]) });
    } else {
      out({ error: `unknown command ${c.cmd}` });
    }
  } catch (e) {
    out({ error: e.message });
  }
}
await mongoose.disconnect();
