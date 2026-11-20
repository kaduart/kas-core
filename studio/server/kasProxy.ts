// Server-side bridge between the Studio in the browser and the KAS process
// routes. It plays the role of the tenant's CRM server:
//   - service tokens stay here (environment), never reach the browser;
//   - occurred_at is stamped here, by the server, never taken from the browser;
//   - only /internal/v1/process/* is reachable (no event consumption, no config);
//   - KAS must be loopback in this pilot.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Connect, Plugin } from 'vite'

export interface TenantBinding {
  tenant: string
  service: string
  tokenEnv: string
}

const DEFAULT_TENANTS =
  'demo-empresa-a=demo-crm-a:DEMO_CRM_A_TOKEN;demo-empresa-b=demo-crm-b:DEMO_CRM_B_TOKEN;demo-clinica=demo-crm-clinica:DEMO_CRM_CLINICA_TOKEN'
const MAX_BODY = 80 * 1024

export function parseTenants(spec: string): TenantBinding[] {
  return spec
    .split(';')
    .map((s) => s.trim())
    .filter(Boolean)
    .map((entry) => {
      const m = /^([A-Za-z0-9][A-Za-z0-9._-]*)=([A-Za-z0-9][A-Za-z0-9._-]*):([A-Z_][A-Z0-9_]*)$/.exec(entry)
      if (!m) throw new Error(`STUDIO_TENANTS: invalid entry "${entry}" (use tenant=service:TOKEN_ENV)`)
      return { tenant: m[1], service: m[2], tokenEnv: m[3] }
    })
}

export function assertLoopback(url: string): URL {
  const u = new URL(url)
  const host = u.hostname.replace(/^\[|\]$/g, '')
  if (!['http:', 'https:'].includes(u.protocol) || !(host === 'localhost' || host === '::1' || /^127\./.test(host))) {
    throw new Error('KAS_URL must be a loopback address in the Studio pilot')
  }
  return u
}

// Allowed process paths (relative to /internal/v1/process/) and methods.
const ROUTES: Array<[string, RegExp]> = [
  ['GET', /^definitions$/],
  ['GET', /^definitions\/[a-z][a-z0-9-]{0,62}$/],
  ['POST', /^definitions:validate$/],
  ['POST', /^definitions$/],
  ['GET', /^instances$/],
  ['POST', /^instances$/],
  ['GET', /^instances\/\d{1,18}$/],
  ['GET', /^tasks$/],
  ['POST', /^tasks\/\d{1,18}\/resolve$/],
  ['POST', /^commands\/[A-Za-z0-9._:-]{1,128}\/query$/],
]

export function allowed(method: string, path: string): boolean {
  return ROUTES.some(([m, re]) => m === method && re.test(path))
}

// stampServerTime replaces any occurred_at with the server clock and pins the
// tenant: the browser cannot choose either.
export function stampBody(raw: string, tenant: string, now: Date): string {
  const body = JSON.parse(raw) as Record<string, unknown>
  if (typeof body !== 'object' || body === null || Array.isArray(body)) throw new Error('body must be an object')
  body.contract_version = 'v1'
  body.tenant_id = tenant
  if ('occurred_at' in body) body.occurred_at = now.toISOString()
  return JSON.stringify(body)
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    let size = 0
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => {
      size += c.length
      if (size > MAX_BODY) {
        reject(new Error('body too large'))
        req.destroy()
        return
      }
      chunks.push(c)
    })
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', reject)
  })
}

function send(res: ServerResponse, status: number, body: unknown) {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json')
  res.setHeader('Cache-Control', 'no-store')
  res.end(JSON.stringify(body))
}

export function kasMiddleware(env: NodeJS.ProcessEnv = process.env): Connect.NextHandleFunction {
  const base = assertLoopback(env.KAS_URL ?? 'http://127.0.0.1:8088')
  const bindings = parseTenants(env.STUDIO_TENANTS ?? DEFAULT_TENANTS)
  return async (req, res, next) => {
    const url = new URL(req.url ?? '/', 'http://studio.local')
    if (url.pathname === '/studio-api/tenants' && req.method === 'GET') {
      send(res, 200, { tenants: bindings.filter((b) => env[b.tokenEnv]).map((b) => b.tenant) })
      return
    }
    const m = /^\/studio-api\/t\/([^/]+)\/(.+)$/.exec(url.pathname)
    if (!m) return next()
    const binding = bindings.find((b) => b.tenant === m[1])
    const token = binding ? env[binding.tokenEnv] : undefined
    const method = req.method ?? 'GET'
    if (!binding || !token) return send(res, 404, { error: { code: 'tenant_not_configured', message: 'tenant is not configured in the Studio' } })
    if (!allowed(method, m[2])) return send(res, 404, { error: { code: 'route_not_allowed', message: 'route is not available in the Studio' } })
    const target = new URL(`/internal/v1/process/${m[2]}`, base)
    url.searchParams.forEach((v, k) => {
      if (k !== 'tenant_id') target.searchParams.set(k, v)
    })
    const headers: Record<string, string> = { 'X-KAS-Service': binding.service, Authorization: `Bearer ${token}` }
    let body: string | undefined
    try {
      if (method === 'POST') {
        body = stampBody(await readBody(req), binding.tenant, new Date())
        headers['Content-Type'] = 'application/json'
      }
      const upstream = await fetch(target, { method, headers, body, signal: AbortSignal.timeout(8000) })
      res.statusCode = upstream.status
      res.setHeader('Content-Type', upstream.headers.get('content-type') ?? 'application/json')
      res.setHeader('Cache-Control', 'no-store')
      res.end(Buffer.from(await upstream.arrayBuffer()))
    } catch (err) {
      send(res, 502, { error: { code: 'kas_unreachable', message: (err as Error).message } })
    }
  }
}

export function kasProxy(): Plugin {
  return {
    name: 'kas-studio-proxy',
    configureServer(server) {
      server.middlewares.use(kasMiddleware())
    },
    configurePreviewServer(server) {
      server.middlewares.use(kasMiddleware())
    },
  }
}
