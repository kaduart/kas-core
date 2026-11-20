import { describe, expect, it } from 'vitest'
import { allowed, assertLoopback, parseTenants, stampBody } from './kasProxy'

describe('kas proxy', () => {
  it('only reaches process routes', () => {
    expect(allowed('GET', 'definitions')).toBe(true)
    expect(allowed('POST', 'tasks/12/resolve')).toBe(true)
    expect(allowed('POST', 'commands/abc/result')).toBe(false) // results come from the CRM, never the Studio
    expect(allowed('PUT', 'roles/comercial/members')).toBe(false)
    expect(allowed('GET', '../events/deliveries/claim')).toBe(false)
  })
  it('stamps occurred_at and tenant on the server', () => {
    const now = new Date('2026-11-20T15:00:00.000Z')
    const out = JSON.parse(stampBody('{"occurred_at":"2030-01-01T00:00:00Z","tenant_id":"outra","outcome":"x"}', 'demo-empresa-a', now))
    expect(out).toEqual({ occurred_at: '2026-11-20T15:00:00.000Z', tenant_id: 'demo-empresa-a', outcome: 'x', contract_version: 'v1' })
    expect(() => stampBody('[1]', 'demo-empresa-a', now)).toThrow()
  })
  it('accepts loopback KAS only', () => {
    expect(assertLoopback('http://127.0.0.1:8088').port).toBe('8088')
    expect(() => assertLoopback('https://kas.example.com')).toThrow()
  })
  it('parses tenant bindings', () => {
    expect(parseTenants('a=svc-a:TOKEN_A;b=svc-b:TOKEN_B')).toEqual([
      { tenant: 'a', service: 'svc-a', tokenEnv: 'TOKEN_A' },
      { tenant: 'b', service: 'svc-b', tokenEnv: 'TOKEN_B' },
    ])
    expect(() => parseTenants('a=svc')).toThrow()
  })
})
