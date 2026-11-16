import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
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

async function stopGo(child) {
  if (!child || child.exitCode !== null) return;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGINT');
  await Promise.race([exited, delay(2500)]);
  if (child.exitCode === null) {
    child.kill('SIGKILL');
    await Promise.race([exited, delay(2500)]);
  }
}

function auditBody(service, tenantId, suffix) {
  return {
    contract_version: 'v1',
    event_id: `evt.registry.${suffix}`,
    tenant_id: tenantId,
    service_id: service,
    actor: 'user.demo-01',
    action: 'settings.updated',
    resource: 'setting.max_meta_keys',
    result: 'success',
    correlation_id: `corr.registry.${suffix}`,
    occurred_at: '2026-11-16T12:00:00Z',
  };
}

test('tenant registry gates configuration and audit across a real Go process', async () => {
  const binaryPath = process.env.KAS_PILOT_BINARY;
  assert.ok(binaryPath, 'npm test must provide the locally built Go executable');
  const tempDir = await mkdtemp(path.join(toolsDir, 'tenant-registry-http-'));
  let child;
  try {
    const config = JSON.parse(await readFile(path.join(pilotDir, 'config.example.json'), 'utf8'));
    config.tenant_registry.find((tenant) => tenant.id === 'tenant-demo-a').status = 'inactive';
    const configPath = path.join(tempDir, 'config.json');
    await writeFile(configPath, JSON.stringify(config), 'utf8');

    const port = await freeLoopbackPort();
    const baseURL = `http://127.0.0.1:${port}`;
    child = spawn(binaryPath, [
      '-config', configPath,
      '-audit-data-dir', path.join(tempDir, 'audit'),
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
    await waitForHealth(child, baseURL);

    async function checkConfig(name, service, token, tenantId, status, code, expectedValue) {
      const response = await fetch(`${baseURL}/internal/v1/config/evaluate`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-kas-service': service, authorization: `Bearer ${token}` },
        body: JSON.stringify({ contract_version: 'v1', tenant_id: tenantId, key: 'logging.max_meta_keys', expected_type: 'integer' }),
      });
      const body = await response.json();
      assert.equal(response.status, status, `${name} config response: ${JSON.stringify(body)}`);
      if (code) assert.equal(body.error?.code, code);
      if (expectedValue !== undefined) assert.equal(body.value, expectedValue);
    }

    await checkConfig('active authorized', 'demo-node-b', tokenB, 'tenant-demo-b', 200, null, 4);
    await checkConfig('inactive', 'fono-node-local', tokenA, 'tenant-demo-a', 403, 'tenant_inactive');
    await checkConfig('unknown', 'fono-node-local', tokenA, 'tenant-unknown', 404, 'tenant_not_found');
    await checkConfig('cross-tenant', 'fono-node-local', tokenA, 'tenant-demo-b', 403, 'tenant_forbidden');

    async function checkAudit(name, service, token, tenantId, suffix, status, code) {
      const response = await fetch(`${baseURL}/internal/v1/audit/events`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-kas-service': service, authorization: `Bearer ${token}` },
        body: JSON.stringify(auditBody(service, tenantId, suffix)),
      });
      const body = await response.json();
      assert.equal(response.status, status, `${name} audit response: ${JSON.stringify(body)}`);
      if (code) assert.equal(body.error?.code, code);
    }

    await checkAudit('active authorized', 'demo-node-b', tokenB, 'tenant-demo-b', 'active', 201);
    await checkAudit('inactive', 'fono-node-local', tokenA, 'tenant-demo-a', 'inactive', 403, 'tenant_inactive');
    await checkAudit('unknown', 'fono-node-local', tokenA, 'tenant-unknown', 'unknown', 404, 'tenant_not_found');
    await checkAudit('cross-tenant', 'fono-node-local', tokenA, 'tenant-demo-b', 'cross', 403, 'tenant_forbidden');

    const lines = (await readFile(path.join(tempDir, 'audit', 'audit.jsonl'), 'utf8')).trim().split('\n');
    assert.deepEqual(lines.map((line) => JSON.parse(line).event_id), ['evt.registry.active']);
  } finally {
    await stopGo(child);
    await rm(tempDir, { recursive: true, force: true });
  }
});
