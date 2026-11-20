import { describe, expect, it } from 'vitest'
import { addStep, autoLayout, describeDeadline, describeDuration, diffDefinitions, edgesOf, positionsOf, referencesTo, removeStep, setDeadline, stepNameProblem, stepTemplate, updateStep, withLayout } from './model'
import type { Definition } from './types'

const def: Definition = {
  schema: 'kas.process/v1',
  process_key: 'retencao-cancelamento',
  start: 'negociar',
  steps: {
    negociar: { type: 'human_task', roles: ['comercial'], outcomes: ['cliente_permanece', 'cliente_cancela'],
      deadline: { business_days: 2, cutoff: '23:59' }, grace: 'PT5M', on_deadline: 'revisar', late_response: 'record_only', next: 'resultado' },
    resultado: { type: 'decision', routes: { cliente_permanece: 'fim_retido', cliente_cancela: 'pedir_cancelamento' } },
    pedir_cancelamento: { type: 'external_command', command_type: 'crm.contract.cancel', on_confirmed: 'fim_cancelado', on_rejected: 'revisar', on_no_reply: 'revisar' },
    revisar: { type: 'human_task', roles: ['supervisor'], outcomes: ['cliente_permanece', 'cliente_cancela'], next: 'resultado' },
    fim_retido: { type: 'end', result: 'retido' },
    fim_cancelado: { type: 'end', result: 'cancelado' },
  },
}

describe('edgesOf', () => {
  it('lists every declared transition with its kind', () => {
    const e = edgesOf(def)
    expect(e.filter((x) => x.from === 'negociar').map((x) => `${x.kind}:${x.to}`)).toEqual(['next:resultado', 'deadline:revisar'])
    expect(e.filter((x) => x.from === 'resultado').map((x) => x.label)).toEqual(['cliente_cancela', 'cliente_permanece'])
    expect(e.filter((x) => x.from === 'pedir_cancelamento').map((x) => x.kind)).toEqual(['confirmed', 'rejected', 'no_reply'])
  })
  it('ignores references to missing steps', () => {
    expect(edgesOf(updateStep(def, 'negociar', { on_deadline: 'nao_existe' })).some((x) => x.to === 'nao_existe')).toBe(false)
  })
})

describe('layout', () => {
  it('places the start first and every step somewhere', () => {
    const p = autoLayout(def)
    expect(p.negociar.x).toBe(0)
    expect(Object.keys(p).sort()).toEqual(Object.keys(def.steps).sort())
    expect(p.resultado.x).toBeGreaterThan(p.negociar.x)
  })
  it('prefers saved positions and round-trips them', () => {
    const saved = withLayout(def, { ...autoLayout(def), negociar: { x: 10.4, y: 20.6 } })
    expect(positionsOf(saved).negociar).toEqual({ x: 10, y: 21 })
  })
})

describe('editing', () => {
  it('changes a deadline without touching other steps', () => {
    const next = setDeadline(def, 'negociar', { business_days: 3, cutoff: '23:59' })
    expect(next.steps.negociar.deadline).toEqual({ business_days: 3, cutoff: '23:59' })
    expect(def.steps.negociar.deadline?.business_days).toBe(2)
    expect(next.steps.revisar).toBe(def.steps.revisar)
  })
  it('drops empty fields so the engine sees a clean definition', () => {
    const next = updateStep(def, 'negociar', { grace: '', roles: [] })
    expect('grace' in next.steps.negociar).toBe(false)
    expect('roles' in next.steps.negociar).toBe(false)
  })
  it('reports the change before publishing', () => {
    const next = withLayout(setDeadline(def, 'negociar', { business_days: 3, cutoff: '23:59' }), autoLayout(def))
    expect(diffDefinitions(def, next)).toEqual([
      { path: 'negociar.deadline.business_days', before: 2, after: 3 },
      { path: 'posições no desenho', before: '', after: 'alteradas' },
    ])
  })
})

describe('describe', () => {
  it('speaks Portuguese', () => {
    expect(describeDeadline({ business_days: 2, cutoff: '23:59' })).toBe('2 dias úteis até 23:59')
    expect(describeDeadline({ business_days: 1 })).toBe('1 dia útil')
    expect(describeDuration('PT5M')).toBe('5 min')
    expect(describeDuration('P1DT2H')).toBe('1 dia 2 horas')
    expect(describeDeadline(undefined)).toBe('sem prazo')
  })
})

describe('addStep', () => {
  it('creates the minimum shape and places it next to the anchor', () => {
    const d = addStep(def, 'ligar_cliente', 'human_task', 'negociar')
    expect(d.steps.ligar_cliente).toEqual({ type: 'human_task', roles: ['responsavel'], outcomes: ['concluido'] })
    const pos = positionsOf(d)
    expect(pos.ligar_cliente.x).toBeGreaterThan(pos.negociar.x)
    expect(def.steps.ligar_cliente).toBeUndefined()
  })
  it('gives every type a template the engine knows', () => {
    for (const t of ['human_task', 'decision', 'external_command', 'wait_event', 'end'] as const) {
      expect(stepTemplate(t).type).toBe(t)
    }
    expect(stepTemplate('external_command').command_type).toMatch(/^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$/)
  })
  it('refuses invalid or repeated names', () => {
    expect(stepNameProblem(def, 'Ligar')).not.toBe('')
    expect(stepNameProblem(def, 'negociar')).toContain('Já existe')
    expect(stepNameProblem(def, 'nova_etapa')).toBe('')
    expect(() => addStep(def, 'revisar', 'end')).toThrow()
  })
})

describe('removeStep', () => {
  it('removes the step and clears every transition that pointed to it', () => {
    const withLay = withLayout(def, positionsOf(def))
    const r = removeStep(withLay, 'revisar')
    expect(r.definition.steps.revisar).toBeUndefined()
    expect(r.cleared).toEqual(['negociar.on_deadline', 'pedir_cancelamento.on_no_reply', 'pedir_cancelamento.on_rejected'])
    expect(r.definition.steps.negociar.on_deadline).toBeUndefined()
    expect(r.definition.steps.negociar.late_response).toBeUndefined()
    expect(r.definition.layout?.nodes?.some((n) => n.id === 'revisar')).toBe(false)
    expect(edgesOf(r.definition).some((e) => e.to === 'revisar')).toBe(false)
  })
  it('clears routes, events and interrupts by key', () => {
    const d: Definition = {
      ...def,
      steps: { ...def.steps, esperar: { type: 'wait_event', on_events: { 'crm.pago': 'fim_retido', 'crm.estorno': 'revisar' } } },
      interrupts: { 'crm.contrato.cancelado': 'fim_cancelado' },
    }
    const r = removeStep(d, 'fim_cancelado')
    expect(r.cleared).toEqual(['interrupts.crm.contrato.cancelado', 'pedir_cancelamento.on_confirmed'])
    expect(r.definition.interrupts).toBeUndefined()
    const r2 = removeStep(d, 'fim_retido')
    expect(r2.cleared).toEqual(['esperar.on_events.crm.pago', 'resultado.routes.cliente_permanece'])
    expect(r2.definition.steps.resultado.routes).toEqual({ cliente_cancela: 'pedir_cancelamento' })
  })
  it('never removes the start step', () => {
    expect(() => removeStep(def, 'negociar')).toThrow(/início/)
    expect(referencesTo(def, 'negociar')).toEqual([])
    expect(referencesTo(def, 'resultado')).toEqual(['negociar.next', 'revisar.next'])
  })
})
