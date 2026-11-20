import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError } from './api'
import { FlowCanvas } from './FlowCanvas'
import { diffDefinitions, fmtDateTime, newPublishId, withLayout } from './model'
import { StepPanel } from './StepPanel'
import type { Definition, DefinitionView } from './types'

type Notice = { kind: 'ok' | 'error' | 'info'; text: string; details?: string[] }

export function Designer({ tenant, onPublished }: { tenant: string; onPublished: () => void }) {
  const [list, setList] = useState<{ process_key: string; latest_version: number; published_at: string }[]>([])
  const [processKey, setProcessKey] = useState<string>()
  const [loaded, setLoaded] = useState<DefinitionView>()
  const [draft, setDraft] = useState<Definition>()
  const [selected, setSelected] = useState<string>()
  const [notice, setNotice] = useState<Notice>()
  const [validated, setValidated] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [showJson, setShowJson] = useState(false)

  const refreshList = useCallback(async () => {
    const defs = await api.definitions(tenant)
    setList(defs)
    return defs
  }, [tenant])

  const open = useCallback(async (key: string, version?: number) => {
    const view = await api.definition(tenant, key, version)
    setProcessKey(key)
    setLoaded(view)
    setDraft(view.definition)
    setSelected(undefined)
    setValidated(undefined)
    setNotice(undefined)
  }, [tenant])

  useEffect(() => {
    setLoaded(undefined)
    setDraft(undefined)
    setProcessKey(undefined)
    refreshList()
      .then((defs) => (defs.length ? open(defs[0].process_key) : undefined))
      .catch((e: Error) => setNotice({ kind: 'error', text: e.message }))
  }, [tenant, refreshList, open])

  const changes = useMemo(() => (loaded && draft ? diffDefinitions(loaded.definition, draft) : []), [loaded, draft])
  const latest = loaded ? Math.max(...loaded.versions) : 0
  const viewingOld = !!loaded && loaded.version !== latest
  const draftKey = draft ? JSON.stringify(draft) : ''

  const edit = (d: Definition) => {
    setDraft(d)
    setValidated(undefined)
    setNotice(undefined)
  }

  const validate = async () => {
    if (!draft) return
    setBusy(true)
    try {
      const r = await api.validate(tenant, draft)
      if (r.valid) {
        setValidated(draftKey)
        setNotice({ kind: 'ok', text: 'Definição válida. Pode publicar.' })
      } else {
        setNotice({ kind: 'error', text: 'Definição inválida', details: r.errors })
      }
    } catch (e) {
      setNotice({ kind: 'error', text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  const publish = async () => {
    if (!draft || !processKey) return
    setBusy(true)
    try {
      const r = await api.publish(tenant, newPublishId(), draft)
      await refreshList()
      await open(processKey)
      setNotice({ kind: 'ok', text: `Versão ${r.version} publicada. Execuções já iniciadas continuam na versão delas.` })
      onPublished()
    } catch (e) {
      const err = e as ApiError
      setNotice({ kind: 'error', text: err.code === 'invalid_definition' ? 'Definição inválida' : err.message, details: [err.message] })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="designer">
      <aside className="sidebar">
        <h2>Fluxos</h2>
        <ul className="list" data-testid="process-list">
          {list.map((d) => (
            <li key={d.process_key}>
              <button className={d.process_key === processKey ? 'active' : ''} onClick={() => open(d.process_key)} data-testid={`open-${d.process_key}`}>
                <strong>{d.process_key}</strong>
                <small>v{d.latest_version} · {fmtDateTime(d.published_at)}</small>
              </button>
            </li>
          ))}
          {list.length === 0 ? <li className="empty">Nenhum fluxo publicado para esta empresa.</li> : null}
        </ul>
      </aside>

      <section className="workspace">
        {loaded && draft ? (
          <>
            <div className="toolbar">
              <div className="toolbar-left">
                <h2>{loaded.process_key}</h2>
                <select value={loaded.version} onChange={(e) => open(loaded.process_key, Number(e.target.value))} data-testid="version-select">
                  {loaded.versions.slice().reverse().map((v) => (
                    <option key={v} value={v}>versão {v}{v === latest ? ' (atual)' : ''}</option>
                  ))}
                </select>
                {viewingOld ? <span className="pill warn">versão antiga: publicar cria a versão {latest + 1}</span> : null}
              </div>
              <div className="toolbar-right">
                <button className="ghost" onClick={() => setShowJson((v) => !v)}>{showJson ? 'Desenho' : 'JSON'}</button>
                <button className="ghost" disabled={!changes.length || busy} onClick={() => edit(loaded.definition)} data-testid="discard">Descartar</button>
                <button disabled={busy} onClick={validate} data-testid="validate">Validar</button>
                <button className="primary" disabled={busy || !changes.length || validated !== draftKey} onClick={publish} data-testid="publish"
                  title={validated !== draftKey ? 'Valide antes de publicar' : undefined}>
                  Publicar nova versão
                </button>
              </div>
            </div>

            {notice ? (
              <div className={`notice ${notice.kind}`} data-testid="notice">
                {notice.text}
                {notice.details?.length ? <ul>{notice.details.map((d) => <li key={d}>{d}</li>)}</ul> : null}
              </div>
            ) : null}

            <div className="editor">
              {showJson ? (
                <pre className="json" data-testid="json">{JSON.stringify(draft, null, 2)}</pre>
              ) : (
                <FlowCanvas definition={draft} selected={selected} editable onSelect={setSelected}
                  onMove={(pos) => edit(withLayout(draft, pos))} />
              )}
              <aside className="panel">
                {selected ? (
                  <StepPanel definition={draft} name={selected} readOnly={false} onChange={edit} />
                ) : (
                  <div className="panel-body muted">
                    <p>Clique numa etapa para editar papéis, prazo, tolerância e caminhos.</p>
                    <p>Publicado por <code>{loaded.published_by}</code> em {fmtDateTime(loaded.published_at)}.</p>
                  </div>
                )}
                <div className="changes" data-testid="changes">
                  <h4>Alterações não publicadas</h4>
                  {changes.length ? (
                    <ul>
                      {changes.map((c) => (
                        <li key={c.path}><code>{c.path}</code>: {String(c.before ?? '—')} → <strong>{String(c.after ?? '—')}</strong></li>
                      ))}
                    </ul>
                  ) : <p className="muted">Nenhuma.</p>}
                </div>
              </aside>
            </div>
          </>
        ) : (
          <div className="empty-state">{notice?.text ?? 'Carregando…'}</div>
        )}
      </section>
    </div>
  )
}
