import type { Definition, DefinitionView, InstanceSummary, InstanceView } from './types'

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message)
  }
}

async function call<T>(tenant: string, path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(`/studio-api/t/${encodeURIComponent(tenant)}/${path}`, {
    method: init?.method ?? 'GET',
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
    body: init?.body ? JSON.stringify(init.body) : undefined,
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } }).error
    throw new ApiError(res.status, err?.code ?? 'error', err?.message ?? `HTTP ${res.status}`)
  }
  return data as T
}

export async function listTenants(): Promise<string[]> {
  const res = await fetch('/studio-api/tenants')
  return ((await res.json()) as { tenants: string[] }).tenants
}

export const api = {
  definitions: (t: string) =>
    call<{ definitions: { process_key: string; latest_version: number; published_at: string }[] }>(t, 'definitions').then((r) => r.definitions),
  definition: (t: string, key: string, version?: number) =>
    call<{ definition: DefinitionView }>(t, `definitions/${key}${version ? `?version=${version}` : ''}`).then((r) => r.definition),
  validate: (t: string, definition: Definition) =>
    call<{ valid: boolean; errors?: string[]; checksum?: string }>(t, 'definitions:validate', {
      method: 'POST', body: { publish_id: 'validate', definition },
    }),
  publish: (t: string, publishId: string, definition: Definition) =>
    call<{ version: number; duplicate: boolean }>(t, 'definitions', { method: 'POST', body: { publish_id: publishId, definition } }),
  instances: (t: string) => call<{ instances: InstanceSummary[] }>(t, 'instances').then((r) => r.instances),
  instance: (t: string, id: number) => call<{ instance: InstanceView }>(t, `instances/${id}`).then((r) => r.instance),
  start: (t: string, processKey: string, businessRef: string, actor: string) =>
    call<{ instance_id: number }>(t, 'instances', {
      method: 'POST',
      body: { process_key: processKey, business_ref: businessRef, start_key: `studio-${businessRef}-${Date.now()}`, actor_id: actor, occurred_at: '' },
    }),
  resolve: (t: string, taskId: number, outcome: string, actor: string, role: string) =>
    call<{ status: string; next_step: string }>(t, `tasks/${taskId}/resolve`, {
      method: 'POST',
      body: { submission_key: `studio-${taskId}-${Date.now()}`, outcome, actor_id: actor, role, occurred_at: '' },
    }),
  queryCommand: (t: string, key: string, actor: string) =>
    call<{ queried: boolean }>(t, `commands/${encodeURIComponent(key)}/query`, { method: 'POST', body: { actor_id: actor } }),
}
