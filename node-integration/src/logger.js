// Isolated copy of backend utils/logger.js at commit be07c465, with one
// opt-in, one-shot KAS shadow comparison added. Node remains authoritative.
import { createKasConfigShadowClient } from './kasConfigShadowClient.mjs';

const MAX_META_KEYS = 10;
const MAX_STRING_LENGTH = 120;

const kasConfigShadow = createKasConfigShadowClient();
if (process.env.KAS_CONFIG_SHADOW_ENABLED === 'true') {
  setImmediate(() => kasConfigShadow.observe({
    tenantId: process.env.KAS_CONFIG_TENANT_ID,
    key: 'logging.max_meta_keys',
    expectedType: 'integer',
    localValue: MAX_META_KEYS,
  }));
}

function sanitizeMeta(meta) {
  if (!meta || typeof meta !== 'object') return {};
  const result = {};
  const entries = Object.entries(meta).slice(0, MAX_META_KEYS);
  for (const [key, value] of entries) {
    if (value == null) { result[key] = value; continue; }
    if (typeof value === 'string') {
      result[key] = value.length > MAX_STRING_LENGTH ? value.slice(0, MAX_STRING_LENGTH) + '...' : value;
      continue;
    }
    if (typeof value === 'number' || typeof value === 'boolean') { result[key] = value; continue; }
    if (typeof value === 'object') {
      result[key] = value._id?.toString?.() || value.id?.toString?.() || value.eventId?.toString?.()
        || value.jobId?.toString?.() || value.correlationId?.toString?.() || '[Object]';
      continue;
    }
    result[key] = '[Unknown]';
  }
  if (Object.keys(meta).length > MAX_META_KEYS) result._truncated = true;
  return result;
}

const logger = {
  info: (message, ...args) => console.log(`[INFO] ${new Date().toISOString()} — ${message}`, ...args.map(sanitizeMeta)),
  error: (message, ...args) => console.error(`[ERROR] ${new Date().toISOString()} — ${message}`, ...args.map(sanitizeMeta)),
  warn: (message, ...args) => console.warn(`[WARN] ${new Date().toISOString()} — ${message}`, ...args.map(sanitizeMeta)),
  debug: (message, ...args) => {
    if (process.env.DEBUG || process.env.NODE_ENV === 'development') {
      console.log(`[DEBUG] ${new Date().toISOString()} — ${message}`, ...args.map(sanitizeMeta));
    }
  },
};

export function createContextLogger(correlationId, component) {
  const prefix = correlationId ? `[${correlationId}]` : '';
  const comp = component ? `[${component}]` : '';
  const write = (level, event, message, meta = {}) => {
    if (level === 'debug' && !(process.env.DEBUG || process.env.NODE_ENV === 'development')) return;
    const line = `${prefix}${comp}[${level.toUpperCase()}] ${event}: ${message}`;
    const output = level === 'error' ? console.error : level === 'warn' ? console.warn : console.log;
    output(line, sanitizeMeta(meta));
  };
  return {
    info: (event, message, meta = {}) => write('info', event, message, meta),
    error: (event, message, meta = {}) => write('error', event, message, meta),
    warn: (event, message, meta = {}) => write('warn', event, message, meta),
    debug: (event, message, meta = {}) => write('debug', event, message, meta),
  };
}

export default logger;
