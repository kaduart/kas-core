// Publishes version 1 of the demo processes and the demo roles for both
// fictitious companies. Uses the service tokens from the environment.
const KAS = process.env.KAS_URL ?? 'http://127.0.0.1:18288'
const companies = [
  { tenant: 'demo-empresa-a', service: 'demo-crm-a', token: process.env.DEMO_CRM_A_TOKEN, days: 2 },
  { tenant: 'demo-empresa-b', service: 'demo-crm-b', token: process.env.DEMO_CRM_B_TOKEN, days: 5 },
]

export function retention(days) {
  return {
    schema: 'kas.process/v1',
    process_key: 'retencao-cancelamento',
    start: 'negociar',
    calendar: { zone: 'America/Sao_Paulo', holidays: ['2026-12-25'] },
    steps: {
      negociar: { type: 'human_task', roles: ['comercial'], outcomes: ['cliente_permanece', 'cliente_cancela'],
        deadline: { business_days: days, cutoff: '23:59' }, grace: 'PT5M', on_deadline: 'revisar', late_response: 'record_only', next: 'resultado' },
      resultado: { type: 'decision', routes: { cliente_permanece: 'fim_retido', cliente_cancela: 'pedir_cancelamento' } },
      pedir_cancelamento: { type: 'external_command', command_type: 'crm.contract.cancel', reply_timeout: 'PT1H', max_status_queries: 2,
        on_confirmed: 'fim_cancelado', on_rejected: 'revisar', on_no_reply: 'revisar', business_status: '910' },
      revisar: { type: 'human_task', roles: ['supervisor'], outcomes: ['cliente_permanece', 'cliente_cancela'], next: 'resultado' },
      fim_retido: { type: 'end', result: 'retido', business_status: '220' },
      fim_cancelado: { type: 'end', result: 'cancelado' },
    },
  }
}

async function call(c, method, path, body) {
  const res = await fetch(`${KAS}/internal/v1/process/${path}`, {
    method,
    headers: { 'X-KAS-Service': c.service, Authorization: `Bearer ${c.token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ contract_version: 'v1', ...body }),
  })
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`)
  return res.json()
}

const from = new Date(Date.now() - 3600_000).toISOString()
for (const c of companies) {
  if (!c.token) throw new Error(`missing token for ${c.service}`)
  const r = await call(c, 'POST', 'definitions', { publish_id: `seed-v1-${c.days}`, definition: retention(c.days) })
  await call(c, 'PUT', 'roles/comercial/members', { change_id: 'seed-comercial', members: ['ator-comercial-1'], valid_from: from })
  await call(c, 'PUT', 'roles/supervisor/members', { change_id: 'seed-supervisor', members: ['ator-supervisor-1'], valid_from: from })
  console.log(`${c.tenant}: retencao-cancelamento v${r.version} (${c.days} dias úteis)`)
}
