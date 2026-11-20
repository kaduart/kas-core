import { describe, expect, it } from 'vitest'
import { autoLayout, describeDeadline, describeDuration, diffDefinitions, edgesOf, positionsOf, setDeadline, updateStep, withLayout } from './model'
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
