import type { Deadline, Definition, LayoutNode, Step, StepType } from './types'

export type EdgeKind = 'next' | 'deadline' | 'route' | 'confirmed' | 'rejected' | 'no_reply' | 'event' | 'interrupt'

export interface FlowEdge {
  id: string
  from: string
  to: string
  kind: EdgeKind
  label: string
}

const KIND_LABEL: Record<EdgeKind, string> = {
  next: '',
  deadline: 'prazo vencido',
  route: '',
  confirmed: 'confirmado',
  rejected: 'recusado',
  no_reply: 'sem resposta',
  event: '',
  interrupt: 'interrupção',
}

// edgesOf lists every transition a definition declares, in a stable order.
export function edgesOf(def: Definition): FlowEdge[] {
  const out: FlowEdge[] = []
  const add = (from: string, to: string | undefined, kind: EdgeKind, label = KIND_LABEL[kind]) => {
    if (!to || !def.steps[to]) return
    out.push({ id: `${from}->${to}:${kind}:${label}`, from, to, kind, label })
  }
  for (const name of Object.keys(def.steps).sort()) {
    const s = def.steps[name]
    add(name, s.next, 'next')
    add(name, s.on_deadline, 'deadline')
    for (const [outcome, to] of Object.entries(s.routes ?? {}).sort()) add(name, to, 'route', outcome)
    add(name, s.on_confirmed, 'confirmed')
    add(name, s.on_rejected, 'rejected')
    add(name, s.on_no_reply, 'no_reply')
    for (const [ev, to] of Object.entries(s.on_events ?? {}).sort()) add(name, to, 'event', ev)
  }
  return out
}

export const NODE_W = 190
export const COL_GAP = 270
export const ROW_GAP = 130

// autoLayout places steps in columns by distance from the start (BFS), so a
// definition without a saved layout still reads left to right.
export function autoLayout(def: Definition): Record<string, { x: number; y: number }> {
  const depth: Record<string, number> = {}
  const order: string[] = []
  const queue = [def.start]
  depth[def.start] = 0
  const edges = edgesOf(def)
  while (queue.length) {
    const cur = queue.shift()!
    order.push(cur)
    for (const e of edges) {
      if (e.from === cur && depth[e.to] === undefined) {
        depth[e.to] = depth[cur] + 1
        queue.push(e.to)
      }
    }
  }
  let maxDepth = Math.max(0, ...Object.values(depth))
  for (const name of Object.keys(def.steps).sort()) {
    if (depth[name] === undefined) {
      depth[name] = ++maxDepth
      order.push(name)
    }
  }
  const rows: Record<number, number> = {}
  const pos: Record<string, { x: number; y: number }> = {}
  for (const name of order) {
    const d = depth[name]
    const r = rows[d] ?? 0
    rows[d] = r + 1
    pos[name] = { x: d * COL_GAP, y: r * ROW_GAP }
  }
  return pos
}

// positionsOf uses the saved layout when it covers a step, auto layout otherwise.
export function positionsOf(def: Definition): Record<string, { x: number; y: number }> {
  const auto = autoLayout(def)
  const saved: Record<string, LayoutNode> = {}
  for (const n of def.layout?.nodes ?? []) if (n && typeof n.id === 'string') saved[n.id] = n
  const out: Record<string, { x: number; y: number }> = {}
  for (const name of Object.keys(def.steps)) {
    const s = saved[name]
    out[name] = s && Number.isFinite(s.x) && Number.isFinite(s.y) ? { x: s.x, y: s.y } : auto[name]
  }
  return out
}

export function withLayout(def: Definition, pos: Record<string, { x: number; y: number }>): Definition {
  const nodes = Object.keys(def.steps)
    .sort()
    .filter((id) => pos[id])
    .map((id) => ({ id, x: Math.round(pos[id].x), y: Math.round(pos[id].y) }))
  return { ...def, layout: { ...(def.layout ?? {}), nodes } }
}

export function updateStep(def: Definition, name: string, patch: Partial<Step>): Definition {
  const cur = def.steps[name]
  if (!cur) throw new Error(`unknown step ${name}`)
  const next: Step = { ...cur, ...patch }
  for (const k of Object.keys(next) as Array<keyof Step>) {
    const v = next[k]
    if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) delete next[k]
  }
  return { ...def, steps: { ...def.steps, [name]: next } }
}

export function setDeadline(def: Definition, name: string, deadline: Deadline | undefined): Definition {
  return updateStep(def, name, { deadline })
}

const ISO = /^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$/

export function describeDuration(iso?: string): string {
  if (!iso) return ''
  const m = ISO.exec(iso)
  if (!m) return iso
  const parts: string[] = []
  const unit = (n: string | undefined, one: string, many: string) => {
    if (n && Number(n) > 0) parts.push(`${Number(n)} ${Number(n) === 1 ? one : many}`)
  }
  unit(m[1], 'dia', 'dias')
  unit(m[2], 'hora', 'horas')
  unit(m[3], 'min', 'min')
  unit(m[4], 's', 's')
  return parts.join(' ') || iso
}

export function describeDeadline(d?: Deadline): string {
  if (!d) return 'sem prazo'
  if (d.business_days) return `${d.business_days} ${d.business_days === 1 ? 'dia útil' : 'dias úteis'}${d.cutoff ? ` até ${d.cutoff}` : ''}`
  return describeDuration(d.duration)
}

export interface Change {
  path: string
  before: unknown
  after: unknown
}

function flatten(v: unknown, prefix: string, out: Record<string, unknown>) {
  if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
    for (const k of Object.keys(v as object).sort()) flatten((v as Record<string, unknown>)[k], prefix ? `${prefix}.${k}` : k, out)
  } else {
    out[prefix] = Array.isArray(v) ? JSON.stringify(v) : v
  }
}

// diffDefinitions lists what changed, ignoring the editor layout, which it
// reports as one line.
export function diffDefinitions(a: Definition, b: Definition): Change[] {
  const fa: Record<string, unknown> = {}
  const fb: Record<string, unknown> = {}
  flatten({ ...a, layout: undefined }, '', fa)
  flatten({ ...b, layout: undefined }, '', fb)
  const keys = Array.from(new Set([...Object.keys(fa), ...Object.keys(fb)])).sort()
  const out: Change[] = keys
    .filter((k) => fa[k] !== fb[k])
    .map((k) => ({ path: k.replace(/^steps\./, ''), before: fa[k], after: fb[k] }))
  if (JSON.stringify(a.layout?.nodes ?? []) !== JSON.stringify(b.layout?.nodes ?? [])) {
    out.push({ path: 'posições no desenho', before: '', after: 'alteradas' })
  }
  return out
}

const TZ = 'America/Sao_Paulo'
export function fmtDateTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return new Intl.DateTimeFormat('pt-BR', {
    timeZone: TZ, weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(d)
}

export const STEP_LABEL: Record<string, string> = {
  human_task: 'Tarefa humana',
  decision: 'Decisão',
  external_command: 'Comando ao CRM',
  wait_event: 'Esperar evento',
  end: 'Fim',
}

export function newPublishId(): string {
  const r = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : Math.random().toString(36).slice(2)
  return `studio-${r}`
}

// Step names follow the engine (internal/process/definition.go namePattern).
export const STEP_NAME = /^[a-z][a-z0-9_]{0,62}$/
export const MAX_STEPS = 100

// Starting shape of a new step. Fields the engine requires but that only the
// person can choose (next step, routes, events) start empty, so Validar points
// them out instead of the editor guessing a path.
export function stepTemplate(type: StepType): Step {
  switch (type) {
    case 'human_task':
      return { type, roles: ['responsavel'], outcomes: ['concluido'] }
    case 'decision':
      return { type, routes: {} }
    case 'external_command':
      return { type, command_type: 'crm.command.nome', reply_timeout: 'PT1H' }
    case 'wait_event':
      return { type, on_events: {} }
    case 'end':
      return { type, result: 'concluido' }
  }
}

// stepNameProblem explains why a name cannot be used, or returns '' when it can.
export function stepNameProblem(def: Definition, name: string): string {
  if (!STEP_NAME.test(name)) return 'Use letras minúsculas, números e _ (começando por letra, até 63).'
  if (def.steps[name]) return `Já existe uma etapa ${name}.`
  if (Object.keys(def.steps).length >= MAX_STEPS) return `O fluxo já tem o máximo de ${MAX_STEPS} etapas.`
  return ''
}

// addStep creates a step of the given type and places it to the right of the
// drawing (or right of `near`), keeping every other position as it is.
export function addStep(def: Definition, name: string, type: StepType, near?: string): Definition {
  const problem = stepNameProblem(def, name)
  if (problem) throw new Error(problem)
  const pos = positionsOf(def)
  const anchor = near && pos[near]
  const maxX = Math.max(0, ...Object.values(pos).map((p) => p.x))
  const at = anchor ? { x: anchor.x + COL_GAP, y: anchor.y + ROW_GAP } : { x: maxX + COL_GAP, y: 0 }
  const next: Definition = { ...def, steps: { ...def.steps, [name]: stepTemplate(type) } }
  return withLayout(next, { ...pos, [name]: at })
}

export interface Removal {
  definition: Definition
  // Every reference that pointed to the removed step and was cleared, as
  // "etapa.campo" (or "interrupts.evento"), for the editor to show.
  cleared: string[]
}

const SCALAR_REFS = ['next', 'on_deadline', 'on_confirmed', 'on_rejected', 'on_no_reply'] as const

// removeStep deletes a step and clears every transition that pointed to it.
// The start step cannot be removed: choose another start first.
export function removeStep(def: Definition, name: string): Removal {
  if (!def.steps[name]) throw new Error(`Etapa ${name} não existe.`)
  if (def.start === name) throw new Error('A etapa de início não pode ser apagada.')
  if (Object.keys(def.steps).length <= 1) throw new Error('O fluxo precisa de pelo menos uma etapa.')
  return clearRefs(def, name)
}

function clearRefs(def: Definition, name: string): Removal {
  const cleared: string[] = []
  const steps: Record<string, Step> = {}
  for (const [sname, s] of Object.entries(def.steps)) {
    if (sname === name) continue
    let cur: Step = s
    for (const k of SCALAR_REFS) {
      if (cur[k] === name) {
        cur = { ...cur }
        delete cur[k]
        if (k === 'on_deadline') delete cur.late_response
        cleared.push(`${sname}.${k}`)
      }
    }
    for (const field of ['routes', 'on_events'] as const) {
      const m = cur[field]
      if (!m) continue
      const hits = Object.keys(m).filter((k) => m[k] === name)
      if (!hits.length) continue
      const rest = { ...m }
      for (const k of hits) {
        delete rest[k]
        cleared.push(`${sname}.${field}.${k}`)
      }
      cur = { ...cur, [field]: rest }
    }
    steps[sname] = cur
  }
  let interrupts = def.interrupts
  if (interrupts) {
    const hits = Object.keys(interrupts).filter((k) => interrupts![k] === name)
    if (hits.length) {
      interrupts = { ...interrupts }
      for (const k of hits) {
        delete interrupts[k]
        cleared.push(`interrupts.${k}`)
      }
    }
  }
  const out: Definition = { ...def, steps, interrupts }
  if (!interrupts || !Object.keys(interrupts).length) delete out.interrupts
  if (def.layout?.nodes) out.layout = { ...def.layout, nodes: def.layout.nodes.filter((n) => n.id !== name) }
  return { definition: out, cleared: cleared.sort() }
}

// referencesTo lists who points to a step, so the editor can warn before removing it.
export function referencesTo(def: Definition, name: string): string[] {
  return clearRefs(def, name).cleared
}
