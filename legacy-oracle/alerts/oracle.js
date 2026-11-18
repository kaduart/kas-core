// Behavioural oracle for the KAS alert center: drives the UNMODIFIED CRM
// alertService.sendAlert with a simulated webhook and an injected clock.
//
// One JSON command per line on stdin, one JSON line on stdout:
//   {"cmd":"raise","nowMs":<ms>,"level":"critical","type":"...","message":"...","details":{...},"sinkDown":false}
//     -> {"sent":bool,"reason":?, "delivered":bool}   delivered = the simulated webhook accepted it
//   {"cmd":"delivered"} -> {"payloads":[...]} everything the webhook accepted
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { register } from 'node:module';
import { createInterface } from 'node:readline';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const legacy = join(here, 'legacy');
for (const line of readFileSync(join(here, 'LEGACY_SHA256SUMS'), 'utf8').trim().split('\n')) {
  const [sum, rel] = line.split(/\s+/);
  const got = createHash('sha256').update(readFileSync(join(legacy, rel))).digest('hex');
  if (got !== sum) { process.stderr.write(`vendored file changed: ${rel}\n`); process.exit(2); }
}

let nowMs = 0;
const RealDate = Date;
Date.now = () => nowMs;
globalThis.Date = class extends RealDate { constructor(...a) { a.length ? super(...a) : super(nowMs); } static now() { return nowMs; } };

let sinkDown = false;
const accepted = [];
let lastAccepted = false;
globalThis.fetch = async (_url, init) => {
  if (sinkDown) return { ok: false, status: 503 };
  accepted.push(JSON.parse(init.body));
  lastAccepted = true;
  return { ok: true, status: 200 };
};
process.env.ALERT_WEBHOOK_URL = 'http://webhook.simulado.invalid/alerts';
delete process.env.SLACK_WEBHOOK_URL;

register(pathToFileURL(join(here, 'shims/hooks.js')));
const { sendAlert } = await import(pathToFileURL(join(legacy, 'infrastructure/alerts/alertService.js')).href);

const rl = createInterface({ input: process.stdin });
for await (const line of rl) {
  const m = JSON.parse(line);
  if (m.cmd === 'raise') {
    nowMs = m.nowMs; sinkDown = !!m.sinkDown; lastAccepted = false;
    const r = await sendAlert({ level: m.level, type: m.type, message: m.message, details: m.details || {} });
    process.stdout.write(JSON.stringify({ sent: r.sent, reason: r.reason || null, delivered: lastAccepted }) + '\n');
  } else if (m.cmd === 'delivered') {
    process.stdout.write(JSON.stringify({ payloads: accepted }) + '\n');
  } else {
    process.stdout.write(JSON.stringify({ error: 'unknown cmd' }) + '\n');
  }
}
process.exit(0);
