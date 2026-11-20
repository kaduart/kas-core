import { useEffect, useState } from 'react'
import { listTenants } from './api'
import { Designer } from './Designer'
import { Executions } from './Executions'

type Tab = 'design' | 'run'

export function App() {
  const [tenants, setTenants] = useState<string[]>([])
  const [tenant, setTenant] = useState<string>()
  const [tab, setTab] = useState<Tab>('design')
  const [published, setPublished] = useState(0)
  const [error, setError] = useState<string>()

  useEffect(() => {
    listTenants()
      .then((t) => {
        setTenants(t)
        setTenant(t[0])
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo">K</span>
          <div>
            <strong>Process Studio</strong>
            <small>piloto · ambiente isolado</small>
          </div>
        </div>
        <nav className="tabs">
          <button className={tab === 'design' ? 'active' : ''} onClick={() => setTab('design')} data-testid="tab-design">Desenho</button>
          <button className={tab === 'run' ? 'active' : ''} onClick={() => setTab('run')} data-testid="tab-run">Execuções</button>
        </nav>
        <label className="tenant">
          <span>Empresa</span>
          <select value={tenant ?? ''} onChange={(e) => setTenant(e.target.value)} data-testid="tenant">
            {tenants.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        </label>
      </header>
      <main>
        {error ? <div className="notice error">{error}</div> : null}
        {!tenant ? <div className="empty-state">Nenhuma empresa configurada no Studio.</div> : null}
        {tenant && tab === 'design' ? <Designer key={tenant} tenant={tenant} onPublished={() => setPublished((n) => n + 1)} /> : null}
        {tenant && tab === 'run' ? <Executions key={tenant} tenant={tenant} refreshToken={published} /> : null}
      </main>
    </div>
  )
}
