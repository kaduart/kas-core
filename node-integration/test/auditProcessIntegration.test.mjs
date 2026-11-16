import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const tokenA = 'a'.repeat(64);
const tokenB = 'b'.repeat(64);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const pilotDir = path.resolve(testDir, '..', '..');
const toolsDir = path.join(pilotDir, '.tools');

async function freeLoopbackPort() {
  const listener = net.createServer();
  await new Promise((resolve, reject) => {
    listener.once('error', reject);
    listener.listen(0, '127.0.0.1', resolve);
  });
  const port = listener.address().port;
  await new Promise((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
  return port;
}

function startGo(binaryPath, dataDir, port) {
  return spawn(binaryPath, [
    '-config', 'config.example.json',
    '-audit-data-dir', dataDir,
    '-addr', `127.0.0.1:${port}`,
  ], {
    cwd: pilotDir,
    env: {
      ...process.env,
      KAS_CALLER_FONO_NODE_TOKEN: tokenA,
      KAS_CALLER_DEMO_NODE_B_TOKEN: tokenB,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  });
}

async function waitForHealth(child, baseURL) {
  let output = '';
  child.stdout.setEncoding('utf8').on('data', (chunk) => { output += chunk; });
  child.stderr.setEncoding('utf8').on('data', (chunk) => { output += chunk; });
  const end = Date.now() + 15_000;
  while (Date.now() < end) {
    if (child.exitCode !== null) throw new Error(`Go exited ${child.exitCode}: ${output}`);
    try {
      if ((await fetch(`${baseURL}/healthz`, { signal: AbortSignal.timeout(300) })).ok) return;
    } catch { /* local process is starting */ }
    await delay(50);
  }
  throw new Error(`Go did not become healthy: ${output}`);
}

async function waitExit(child, timeoutMs = 5000) {
  if (child.exitCode !== null) return child.exitCode;
  return Promise.race([
    new Promise((resolve) => child.once('exit', (code) => resolve(code))),
    delay(timeoutMs).then(() => null),
  ]);
}

async function stopGo(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGINT');
  if (await waitExit(child, 2500) === null) {
    child.kill();
    await waitExit(child, 2500);
  }
}

function auditBody({ tenantId = 'tenant-demo-a', eventId }) {
  return {
    contract_version: 'v1',
    event_id: eventId,
    tenant_id: tenantId,
    service_id: 'fono-node-local',
    actor: 'user.demo-01',
    action: 'settings.updated',
    resource: 'setting.max_meta_keys',
    result: 'success',
    correlation_id: 'corr.demo-01',
    occurred_at: '2026-11-16T12:00:00Z',
  };
}

async function sendAudit(baseURL, body, token = tokenA) {
  return fetch(`${baseURL}/internal/v1/audit/events`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-kas-service': 'fono-node-local',
      authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(body),
  });
}

test('real audit HTTP writes, enforces tenant boundaries and survives process restart', async () => {
  const binaryPath = process.env.KAS_PILOT_BINARY;
  assert.ok(binaryPath, 'npm test must provide the locally built Go executable');
  const dataDir = await mkdtemp(path.join(toolsDir, 'audit-http-test-'));
  let child;
  try {
    let port = await freeLoopbackPort();
    let baseURL = `http://127.0.0.1:${port}`;
    child = startGo(binaryPath, dataDir, port);
    await waitForHealth(child, baseURL);

    const accepted = await sendAudit(baseURL, auditBody({ eventId: 'evt.http-01' }));
    assert.equal(accepted.status, 201);
    const acceptedBody = await accepted.json();
    assert.equal(acceptedBody.event.service_id, 'fono-node-local');
    assert.equal(acceptedBody.event.actor, 'user.demo-01');
    assert.ok(acceptedBody.event.received_at);

    const crossTenant = await sendAudit(baseURL, auditBody({ tenantId: 'tenant-demo-b', eventId: 'evt.cross-01' }));
    assert.equal(crossTenant.status, 403);
    assert.equal((await crossTenant.json()).error.code, 'tenant_forbidden');

    const competitorPort = await freeLoopbackPort();
    const competitor = startGo(binaryPath, dataDir, competitorPort);
    const competitorExit = await waitExit(competitor, 5000);
    assert.equal(competitorExit, 2, 'second process must fail closed on the held audit directory');

    await stopGo(child);
    child = null;
    const lockPath = path.join(dataDir, 'audit.lock');
    // If the platform does not deliver SIGINT as a graceful signal, the owner
    // is confirmed stopped above; the test operator clears its stale lock.
    await rm(lockPath, { force: true });

    port = await freeLoopbackPort();
    baseURL = `http://127.0.0.1:${port}`;
    child = startGo(binaryPath, dataDir, port);
    await waitForHealth(child, baseURL);
    const lines = (await readFile(path.join(dataDir, 'audit.jsonl'), 'utf8')).trim().split('\n');
    assert.equal(lines.length, 1);
    assert.equal(JSON.parse(lines[0]).event_id, 'evt.http-01');

    const afterRestart = await sendAudit(baseURL, auditBody({ eventId: 'evt.http-02' }));
    assert.equal(afterRestart.status, 201);
    await stopGo(child);
    child = null;
    const finalLines = (await readFile(path.join(dataDir, 'audit.jsonl'), 'utf8')).trim().split('\n');
    assert.deepEqual(finalLines.map((line) => JSON.parse(line).event_id), ['evt.http-01', 'evt.http-02']);
  } finally {
    if (child) await stopGo(child);
    await rm(dataDir, { recursive: true, force: true });
  }
});
