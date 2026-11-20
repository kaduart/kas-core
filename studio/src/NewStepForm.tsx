import { useState } from 'react'
import { addStep, STEP_LABEL, stepNameProblem } from './model'
import type { Definition, StepType } from './types'

const TYPES: StepType[] = ['human_task', 'decision', 'external_command', 'wait_event', 'end']

// NewStepForm creates a step with the engine's minimum shape. The paths that
// lead into and out of it are chosen afterwards in the step panel.
export function NewStepForm({ definition, near, onCreate, onCancel }: {
  definition: Definition
  near?: string
  onCreate: (def: Definition, name: string) => void
  onCancel: () => void
}) {
  const [name, setName] = useState('')
  const [type, setType] = useState<StepType>('human_task')
  const trimmed = name.trim()
  const problem = trimmed ? stepNameProblem(definition, trimmed) : ''
  const submit = () => {
    if (!trimmed || problem) return
    onCreate(addStep(definition, trimmed, type, near), trimmed)
  }
  return (
    <form className="panel-body" data-testid="new-step-form" onSubmit={(e) => { e.preventDefault(); submit() }}>
      <div className="panel-title"><h3>Nova etapa</h3></div>
      <label className="field">
        <span>Nome (minúsculas, números e _)</span>
        <input value={name} autoFocus data-testid="new-step-name" placeholder="ex.: ligar_cliente" onChange={(e) => setName(e.target.value)} />
      </label>
      {problem ? <p className="hint error" data-testid="new-step-problem">{problem}</p> : null}
      <label className="field">
        <span>Tipo</span>
        <select value={type} data-testid="new-step-type" onChange={(e) => setType(e.target.value as StepType)}>
          {TYPES.map((t) => <option key={t} value={t}>{STEP_LABEL[t]}</option>)}
        </select>
      </label>
      <p className="hint">
        {near ? <>A etapa aparece ao lado de <code>{near}</code>. </> : null}
        Depois de criar, ligue os caminhos no painel. Ninguém chega a ela até outra etapa apontar para ela, e o Validar avisa.
      </p>
      <div className="row">
        <button type="submit" className="primary" disabled={!trimmed || !!problem} data-testid="new-step-create">Criar etapa</button>
        <button type="button" className="ghost" onClick={onCancel}>Cancelar</button>
      </div>
    </form>
  )
}
