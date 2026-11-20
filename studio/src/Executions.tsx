import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import { FlowCanvas } from './FlowCanvas'
import { describeDeadline, fmtDateTime } from './model'
import type { Definition, InstanceSummary, InstanceView } from './types'

// Fictitious actors of the pilot, one per role.
const DEMO_ACTOR: Record<string, string> = { comercial: 'ator-comercial-1', supervisor: 'ator-supervisor-1' }

const HISTORY_LABEL: Record<string, string> = {
  started: 'Execução iniciada',
  entered: 'Entrou na etapa',
  task_resolved: 'Tarefa resolvida',
  response_late: 'Resposta fora do prazo (registrada para revisão)',
  response_superseded: 'Resposta substituída',
  response_rejected: 'Resposta recusada',
  late_response: 'Resposta tardia registrada',
  deadline_expired: 'Prazo vencido',
  command_requested: 'Comando enviado ao CRM',
  command_status_query: 'Consulta de status ao CRM',
  command_query_requested: 'Nova consulta pedida',
  command_unknown: 'Resultado do CRM desconhecido',
  command_confirmed: 'CRM confirmou',
  command_rejected: 'CRM recusou',
  command_result: 'Resultado do CRM registrado',
  wait_opened: 'Esperando evento',
  event_matched: 'Evento recebido',
  event_late: 'Evento depois do prazo',
  event_stale: 'Evento antigo ignorado',
  event_wait_closed: 'Evento para espera já fechada',
  interrupted: 'Interrompida',
  timer_executed: 'Timer executado',
  completed: 'Execução concluída',
}

function useDefinitionVersion(tenant: string, key?: string, version?: number) {
  const [def, setDef] = useState<Definition>()
  useEffect(() => {
    if (!key || !version) return
    let alive = true
    api.definition(tenant, key, version).then((v) => alive && setDef(v.definition)).catch(() => undefined)
    return () => { alive = false }
  }, [tenant, key, version])
  return def
}

export function Executions({ tenant, refreshToken }: { tenant: string; refreshToken: number }) {
  const [processes, setProcesses] = useState<string[]>([])
  const [list, setList] = useState<InstanceSummary[]>([])
  const [selectedId, setSelectedId] = useState<number>()
  const [inst, setInst] = useState<InstanceView>()
  const [processKey, setProcessKey] = useState('')
  const [ref, setRef] = useState('')
  const [error, setError] = useState<string>()
  const [actors, setActors] = useState<Record<number, string>>({})
  const def = useDefinitionVersion(tenant, inst?.process_key, inst?.version)

  const refresh = useCallback(async () => {
    const [items, defs] = await Promise.all([api.instances(tenant), api.definitions(tenant)])
    setList(items)
    setProcesses(defs.map((d) => d.process_key))
    setProcessKey((k) => k || defs[0]?.process_key || '')
    if (selectedId) setInst(await api.instance(tenant, selectedId))
  }, [tenant, selectedId])

  useEffect(() => {
    setSelectedId(undefined)
    setInst(undefined)
  }, [tenant])

  useEffect(() => {
    refresh().catch((e: Error) => setError(e.message))
    const t = setInterval(() => refresh().catch(() => undefined), 1500)
    return () => clearInterval(t)
  }, [refresh, refreshToken])

  const start = async () => {
    setError(undefined)
    try {
      const r = await api.start(tenant, processKey, ref.trim(), 'ator-comercial-1')
      setRef('')
      setSelectedId(r.instance_id)
      setInst(await api.instance(tenant, r.instance_id))
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const resolve = async (taskId: number, outcome: string, role: string) => {
    setError(undefined)
    try {
      await api.resolve(tenant, taskId, outcome, actors[taskId] ?? DEMO_ACTOR[role] ?? '', role)
      await refresh()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="executions">
      <aside className="sidebar">
        <h2>Nova execução</h2>
        <div className="start-form">
          <select value={processKey} onChange={(e) => setProcessKey(e.target.value)} data-testid="start-process">
            {processes.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
          <input placeholder="referência (ex. contrato-demo-10)" value={ref} onChange={(e) => setRef(e.target.value)} data-testid="start-ref" />
          <button className="primary" disabled={!processKey || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(ref.trim())} onClick={start} data-testid="start">
            Iniciar
          </button>
        </div>
        <h2>Execuções</h2>
        <ul className="list" data-testid="instance-list">
          {list.map((i) => (
            <li key={i.id}>
              <button className={i.id === selectedId ? 'active' : ''} onClick={() => setSelectedId(i.id)} data-testid={`instance-${i.business_ref}`}>
                <strong>{i.business_ref}</strong>
                <small>{i.process_key} · v{i.version} · {i.status === 'completed' ? `fim: ${i.result}` : i.current_step}</small>
              </button>
            </li>
          ))}
          {list.length === 0 ? <li className="empty">Nenhuma execução ainda.</li> : null}
        </ul>
      </aside>

      <section className="workspace">
        {error ? <div className="notice error" data-testid="exec-error">{error}</div> : null}
        {inst ? (
          <div className="instance" data-testid="instance-detail">
            <div className="toolbar">
              <div className="toolbar-left">
                <h2>{inst.business_ref}</h2>
                <span className="pill" data-testid="instance-version">versão {inst.version}</span>
                <span className={`pill ${inst.status === 'completed' ? 'ok' : 'live'}`} data-testid="instance-status">
                  {inst.status === 'completed' ? `concluída: ${inst.result}` : `em andamento: ${inst.current_step}`}
                </span>
              </div>
            </div>
            <div className="editor">
              {def ? <FlowCanvas definition={def} current={inst.status === 'running' ? inst.current_step : undefined} /> : <div className="canvas" />}
              <aside className="panel">
                <div className="panel-body">
                  <h4>Tarefas</h4>
                  {(inst.tasks ?? []).map((t) => {
                    const role = t.roles[0] ?? ''
                    const stepDef = def?.steps[t.step]
                    return (
                      <div className={`task ${t.status}`} key={t.id} data-testid={`task-${t.step}`}>
                        <div className="task-head"><strong>{t.step}</strong><span className="pill">{t.status}</span></div>
                        <div className="task-meta">
                          papéis: {t.roles.join(', ')}
                          {stepDef?.deadline ? <> · prazo da versão: {describeDeadline(stepDef.deadline)}</> : null}
                        </div>
                        {t.due_at ? <div className="task-due" data-testid={`due-${t.step}`}>vence {fmtDateTime(t.due_at)}</div> : null}
                        {t.outcome ? <div className="task-meta">resposta: <code>{t.outcome}</code></div> : null}
                        {t.status === 'open' ? (
                          <div className="task-actions">
                            <input value={actors[t.id] ?? DEMO_ACTOR[role] ?? ''} onChange={(e) => setActors({ ...actors, [t.id]: e.target.value })}
                              aria-label="ator" data-testid={`actor-${t.step}`} />
                            {t.outcomes.map((o) => (
                              <button key={o} onClick={() => resolve(t.id, o, role)} data-testid={`resolve-${t.step}-${o}`}>{o}</button>
                            ))}
                          </div>
                        ) : null}
                      </div>
                    )
                  })}
                  {(inst.commands ?? []).length ? <h4>Comandos ao CRM</h4> : null}
                  {(inst.commands ?? []).map((c) => (
                    <div className="command" key={c.command_key} data-testid={`command-${c.step}`}>
                      <code>{c.command_type}</code> <span className={`pill cmd-${c.status}`}>{c.status}</span>
                      <small>geração {c.generation} · tentativa {c.attempt} · consultas {c.status_queries}</small>
                    </div>
                  ))}
                  <h4>Histórico</h4>
                  <ol className="history" data-testid="history">
                    {(inst.history ?? []).map((h) => (
                      <li key={h.id}>
                        <span className="when">{fmtDateTime(h.recorded_at)}</span>
                        <span>{HISTORY_LABEL[h.kind] ?? h.kind}{h.step ? `: ${h.step}` : ''}{h.actor_id ? ` (${h.actor_id})` : ''}</span>
                      </li>
                    ))}
                  </ol>
                </div>
              </aside>
            </div>
          </div>
        ) : (
          <div className="empty-state">Escolha ou inicie uma execução. O CRM simulado responde aos comandos sozinho.</div>
        )}
      </section>
    </div>
  )
}
