import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { createKasConfigShadowClient } from '../src/kasConfigShadowClient.mjs';

const tokenA = 'a'.repeat(64);
const tokenB = 'b'.repeat(64);
const key = 'logging.max_meta_keys';
const integrationDir = path.dirname(fileURLToPath(import.meta.url));
const pilotDir = path.resolve(integrationDir, '..', '..');

async function freeLoopbackPort() {
  const listener = net.createServer();
  await new Promise((resolve, reject) => {
    listener.once('error', reject);
    listener.listen(0, '127.0.0.1', resolve);
  });
  const address = listener.address();
  await new Promise((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
  return address.port;
}

async function waitForHealth(child, baseURL) {
  const expiresAt = Date.now() + 30_000;
  let lastFailure = 'no response';
  while (Date.now() < expiresAt) {
    if (child.exitCode !== null) throw new Error('Go process exited early with code ' + child.exitCode);
    try {
      const response = await fetch(baseURL + '/healthz', { signal: AbortSignal.timeout(250) });
      if (response.ok) return;
    } catch (error) { lastFailure = error?.cause?.code || error?.name || 'request failed'; }
    await delay(40);
  }
  throw new Error(`Go process did not become healthy within 30 seconds (last request: ${lastFailure})`);
}

async function stopProcess(child) {
  if (child.exitCode !== null) return;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill();
  await Promise.race([exited, delay(1500)]);
  if (child.exitCode === null) {
    child.kill('SIGKILL');
    await Promise.race([exited, delay(1500)]);
  }
}

function request(baseURL, { service, token, tenantId, requestedKey = key, expectedType = 'integer' }) {
  return fetch(baseURL + '/internal/v1/config/evaluate', {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-kas-service': service,
      authorization: 'Bearer ' + token,
    },
    body: JSON.stringify({
      contract_version: 'v1',
      tenant_id: tenantId,
      key: requestedKey,
      expected_type: expectedType,
    }),
  });
}

test('real Node client talks to Go process and falls back when it stops', async (t) => {
  const binaryPath = process.env.KAS_PILOT_BINARY;
  assert.ok(binaryPath, 'npm test must provide the locally built Go executable');
  const dataDir = mkdtempSync(path.join(pilotDir, '.tools', 'node-shadow-audit-'));
  t.after(() => rmSync(dataDir, { recursive: true, force: true }));
  const port = await freeLoopbackPort();
  const addr = '127.0.0.1:' + port;
  const baseURL = 'http://' + addr;
  const child = spawn(binaryPath, [
    '-config', 'config.example.json',
    '-audit-data-dir', dataDir,
    '-addr', addr,
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
  let childOutput = '';
  child.stdout.setEncoding('utf8').on('data', (chunk) => { childOutput += chunk; });
  child.stderr.setEncoding('utf8').on('data', (chunk) => { childOutput += chunk; });

  try {
    try {
      await waitForHealth(child, baseURL);
    } catch (error) {
      throw new Error(`${error.message}; Go output: ${childOutput || '(empty)'}`);
    }

    const valid = await request(baseURL, { service: 'fono-node-local', token: tokenA, tenantId: 'tenant-demo-a' });
    assert.equal(valid.status, 200);
    assert.deepEqual(await valid.json().then(({ value, type, contract_version }) => ({ value, type, contract_version })), {
      value: 10, type: 'integer', contract_version: 'v1',
    });

    const crossTenant = await request(baseURL, { service: 'fono-node-local', token: tokenA, tenantId: 'tenant-demo-b' });
    assert.equal(crossTenant.status, 403);
    assert.equal((await crossTenant.json()).error.code, 'tenant_forbidden');

    const badCredential = await request(baseURL, { service: 'fono-node-local', token: 'c'.repeat(64), tenantId: 'tenant-demo-a' });
    assert.equal(badCredential.status, 401);
    assert.equal((await badCredential.json()).error.code, 'unauthorized');

    const unknownKey = await request(baseURL, { service: 'fono-node-local', token: tokenA, tenantId: 'tenant-demo-a', requestedKey: 'unknown.setting' });
    assert.equal(unknownKey.status, 422);
    assert.equal((await unknownKey.json()).error.code, 'unknown_key');

    const tenantBValid = await request(baseURL, { service: 'demo-node-b', token: tokenB, tenantId: 'tenant-demo-b' });
    assert.equal(tenantBValid.status, 200, `tenant B valid evaluation: ${await tenantBValid.text()}`);

    const observations = [];
    const localValue = 10;
    const shadow = createKasConfigShadowClient({
      env: {
        KAS_CONFIG_SHADOW_ENABLED: 'true',
        KAS_CONFIG_SHADOW_URL: baseURL,
        KAS_CONFIG_SERVICE_ID: 'demo-node-b',
        KAS_CONFIG_SERVICE_TOKEN: tokenB,
      },
      onObservation: (observation) => observations.push(observation),
    });
    assert.equal(shadow.observe({
      tenantId: 'tenant-demo-b',
      key,
      expectedType: 'integer',
      localValue,
    }), 'scheduled');
    await shadow.flush();
    assert.deepEqual(observations.map(({ key: observedKey, status, equal, errorCode, httpStatus }) => ({ key: observedKey, status, equal, errorCode, httpStatus })), [
      { key, status: 'compared', equal: false, errorCode: undefined, httpStatus: undefined },
    ]);
    assert.equal(localValue, 10, 'shadow comparison must not replace the local Node decision');
  } finally {
    await stopProcess(child);
  }

  const outageObservations = [];
  const localValueDuringOutage = 10;
  const offlineShadow = createKasConfigShadowClient({
    env: {
      KAS_CONFIG_SHADOW_ENABLED: 'true',
      KAS_CONFIG_SHADOW_URL: baseURL,
      KAS_CONFIG_SERVICE_ID: 'fono-node-local',
      KAS_CONFIG_SERVICE_TOKEN: tokenA,
    },
    onObservation: (observation) => outageObservations.push(observation),
  });
  assert.equal(offlineShadow.observe({
    tenantId: 'tenant-demo-a',
    key,
    expectedType: 'integer',
    localValue: localValueDuringOutage,
  }), 'scheduled');
  await offlineShadow.flush();
  assert.equal(outageObservations[0].status, 'unavailable');
  assert.equal(localValueDuringOutage, 10, 'Node remains usable when Go is unavailable');
});
