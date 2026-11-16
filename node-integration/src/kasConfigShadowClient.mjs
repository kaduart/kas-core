const correlationIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

function matchesType(value, expectedType) {
  // Go values are int64; JavaScript can compare only safe integers exactly.
  if (expectedType === 'integer') return Number.isSafeInteger(value);
  if (expectedType === 'boolean') return typeof value === 'boolean';
  if (expectedType === 'string') return typeof value === 'string';
  return false;
}

function validCorrelationID(value) {
  return typeof value === 'string' && correlationIDPattern.test(value);
}

function safeTarget(rawURL) {
  const url = new URL(rawURL);
  const loopback = ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname);
  if (url.username || url.password || (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback))) {
    throw new Error('KAS config URL must use HTTPS except for loopback development');
  }
  return new URL('/internal/v1/config/evaluate', url);
}

function resultCode(error) {
  if (error?.name === 'AbortError' || error?.name === 'TimeoutError') return 'timeout';
  return 'unavailable';
}

export function createKasConfigShadowClient({
  env = process.env,
  fetchImpl = globalThis.fetch,
  onObservation = (observation) => console.info('[KASConfigShadow]', observation),
  timeoutMs = 150,
  maxInFlight = 4,
} = {}) {
  const pending = new Set();
  const boundedTimeoutMs = Number.isFinite(timeoutMs) ? Math.min(1000, Math.max(10, Math.floor(timeoutMs))) : 150;
  const boundedMaxInFlight = Number.isFinite(maxInFlight) ? Math.min(16, Math.max(1, Math.floor(maxInFlight))) : 4;
  const report = (observation) => {
    try { onObservation(observation); } catch { /* diagnostics must never affect the caller */ }
  };

  const observe = ({ tenantId, key, expectedType, localValue, correlationId } = {}) => {
    if (env.KAS_CONFIG_SHADOW_ENABLED !== 'true') return 'disabled';
    if (!tenantId || !key || !matchesType(localValue, expectedType) || typeof fetchImpl !== 'function') {
      report({ key: key || 'unknown', status: 'skipped_context' });
      return 'skipped_context';
    }
    if (pending.size >= boundedMaxInFlight) {
      report({ key, status: 'skipped_capacity' });
      return 'skipped_capacity';
    }

    let endpoint;
    try {
      endpoint = safeTarget(env.KAS_CONFIG_SHADOW_URL || 'http://127.0.0.1:8088');
    } catch {
      report({ key, status: 'invalid_endpoint' });
      return 'invalid_endpoint';
    }

    const serviceId = env.KAS_CONFIG_SERVICE_ID;
    const token = env.KAS_CONFIG_SERVICE_TOKEN;
    if (!serviceId || !token) {
      report({ key, status: 'skipped_credentials' });
      return 'skipped_credentials';
    }

    const task = (async () => {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), boundedTimeoutMs);
      const started = Date.now();
      try {
        const response = await fetchImpl(endpoint, {
          method: 'POST',
          headers: {
            'content-type': 'application/json',
            'x-kas-service': serviceId,
            authorization: 'Bearer ' + token,
            ...(validCorrelationID(correlationId) ? { 'x-correlation-id': correlationId } : {}),
          },
          body: JSON.stringify({
            contract_version: 'v1',
            tenant_id: tenantId,
            key,
            expected_type: expectedType,
          }),
          signal: controller.signal,
        });
        if (!response.ok) {
          let code = 'unavailable';
          try { code = (await response.json())?.error?.code || code; } catch { /* do not log response bodies */ }
          report({ key, status: 'rejected', httpStatus: response.status, errorCode: code, latencyMs: Date.now() - started });
          return;
        }
        const body = await response.json();
        const valid = body?.contract_version === 'v1'
          && body?.tenant_id === tenantId
          && body?.key === key
          && body?.type === expectedType
          && matchesType(body?.value, expectedType);
        if (!valid) {
          report({ key, status: 'invalid_response', latencyMs: Date.now() - started });
          return;
        }
        report({ key, status: 'compared', equal: body.value === localValue, latencyMs: Date.now() - started });
      } catch (error) {
        report({ key, status: resultCode(error), latencyMs: Date.now() - started });
      } finally {
        clearTimeout(timer);
      }
    })();

    pending.add(task);
    task.finally(() => pending.delete(task));
    return 'scheduled';
  };

  const flush = async () => { await Promise.all([...pending]); };
  return { observe, flush };
}
