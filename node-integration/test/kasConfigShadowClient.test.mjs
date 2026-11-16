import test from 'node:test';
import assert from 'node:assert/strict';
import { createKasConfigShadowClient } from '../src/kasConfigShadowClient.mjs';

const token = 'a'.repeat(64);
const env = (extra = {}) => ({
  KAS_CONFIG_SHADOW_ENABLED: 'true',
  KAS_CONFIG_SHADOW_URL: 'http://127.0.0.1:8088',
  KAS_CONFIG_SERVICE_ID: 'fono-node-local',
  KAS_CONFIG_SERVICE_TOKEN: token,
  KAS_CONFIG_TENANT_ID: 'tenant-demo-a',
  ...extra,
});
const demoInput = { tenantId: 'tenant-demo-a', key: 'logging.max_meta_keys', expectedType: 'integer', localValue: 10 };

test('disabled mode performs no call', async () => {
  let calls = 0;
  const client = createKasConfigShadowClient({ env: env({ KAS_CONFIG_SHADOW_ENABLED: 'false' }), fetchImpl: async () => { calls++; } });
  assert.equal(client.observe(demoInput), 'disabled');
  await client.flush();
  assert.equal(calls, 0);
});

test('shadow mode sends the contract and reports parity without exposing credentials or value', async () => {
  const observations = [];
  let received;
  const client = createKasConfigShadowClient({
    env: env(),
    onObservation: (value) => observations.push(value),
    fetchImpl: async (_url, options) => {
      received = options;
      return new Response(JSON.stringify({ contract_version: 'v1', config_version: 'fixture', tenant_id: 'tenant-demo-a', key: 'logging.max_meta_keys', type: 'integer', value: 10 }), { status: 200, headers: { 'content-type': 'application/json' } });
    },
  });
  assert.equal(client.observe(demoInput), 'scheduled');
  await client.flush();
  assert.deepEqual(JSON.parse(received.body), { contract_version: 'v1', tenant_id: 'tenant-demo-a', key: 'logging.max_meta_keys', expected_type: 'integer' });
  assert.equal(received.headers.authorization, `Bearer ${token}`);
  assert.deepEqual(observations, [{ key: 'logging.max_meta_keys', status: 'compared', equal: true, latencyMs: observations[0].latencyMs }]);
  assert.equal(JSON.stringify(observations).includes(token), false);
  assert.equal(JSON.stringify(observations).includes('"value"'), false);
});

test('unavailable Go service is reported and local decision remains untouched', async () => {
  const observations = [];
  const localValue = 10;
  const client = createKasConfigShadowClient({ env: env(), onObservation: (value) => observations.push(value), fetchImpl: async () => { throw new TypeError('network unavailable'); } });
  assert.equal(client.observe({ ...demoInput, localValue }), 'scheduled');
  await client.flush();
  assert.equal(localValue, 10);
  assert.equal(observations[0].status, 'unavailable');
  assert.equal(JSON.stringify(observations).includes('network unavailable'), false);
});

test('timeout is bounded and reported without retry', async () => {
  const observations = [];
  let calls = 0;
  const client = createKasConfigShadowClient({
    env: env(), timeoutMs: 20, onObservation: (value) => observations.push(value),
    fetchImpl: (_url, options) => {
      calls++;
      return new Promise((_resolve, reject) => {
        options.signal.addEventListener('abort', () => {
          reject(Object.assign(new Error('aborted'), { name: 'AbortError' }));
        });
      });
    },
  });
  assert.equal(client.observe(demoInput), 'scheduled');
  await client.flush();
  assert.equal(calls, 1);
  assert.equal(observations[0].status, 'timeout');
});

test('in-flight work is capped and excess shadow calls are skipped', async () => {
  let calls = 0;
  let release;
  const observations = [];
  const client = createKasConfigShadowClient({
    env: env(), maxInFlight: 1, onObservation: (value) => observations.push(value),
    fetchImpl: async () => { calls++; await new Promise((resolve) => { release = resolve; }); return new Response('{}', { status: 503 }); },
  });
  assert.equal(client.observe(demoInput), 'scheduled');
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(client.observe(demoInput), 'skipped_capacity');
  release();
  await client.flush();
  assert.equal(calls, 1);
  assert.equal(observations.some((item) => item.status === 'skipped_capacity'), true);
});

test('rejects a non-HTTPS remote endpoint before calling fetch', () => {
  let calls = 0;
  const client = createKasConfigShadowClient({ env: env({ KAS_CONFIG_SHADOW_URL: 'http://example.invalid' }), onObservation: () => {}, fetchImpl: async () => { calls++; } });
  assert.equal(client.observe(demoInput), 'invalid_endpoint');
  assert.equal(calls, 0);
});

test('forwards only correlation IDs accepted by the Go endpoint', async () => {
  const receivedHeaders = [];
  const client = createKasConfigShadowClient({
    env: env(),
    onObservation: () => {},
    fetchImpl: async (_url, options) => {
      receivedHeaders.push(options.headers);
      return new Response(JSON.stringify({ contract_version: 'v1', tenant_id: 'tenant-demo-a', key: 'logging.max_meta_keys', type: 'integer', value: 10 }), { status: 200 });
    },
  });

  for (const [correlationId, expected] of [
    ['c'.repeat(64), 'c'.repeat(64)],
    ['c'.repeat(65), undefined],
    ['invalid correlation', undefined],
  ]) {
    assert.equal(client.observe({ ...demoInput, correlationId }), 'scheduled');
    await client.flush();
    assert.equal(receivedHeaders.at(-1)['x-correlation-id'], expected);
  }
});

test('skips integer values JavaScript cannot compare exactly', async () => {
  let calls = 0;
  const client = createKasConfigShadowClient({
    env: env(),
    onObservation: () => {},
    fetchImpl: async () => { calls++; },
  });

  assert.equal(client.observe({ ...demoInput, localValue: Number.MAX_SAFE_INTEGER + 1 }), 'skipped_context');
  await client.flush();
  assert.equal(calls, 0);
});
