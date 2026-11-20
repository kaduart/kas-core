import { useEffect, useState } from 'react'
import { describeDeadline, STEP_LABEL, updateStep } from './model'
import type { Deadline, Definition, Step } from './types'

interface Props {
  definition: Definition
  name: string
  readOnly: boolean
  onChange: (def: Definition) => void
}

function StepSelect({ value, steps, onChange, testId, disabled, allowEmpty }: {
  value?: string; steps: string[]; onChange: (v: string) => void; testId: string; disabled: boolean; allowEmpty?: boolean
}) {
  return (
    <select value={value ?? ''} onChange={(e) => onChange(e.target.value)} data-testid={testId} disabled={disabled}>
      {allowEmpty || !value ? <option value="">—</option> : null}
      {steps.map((s) => <option key={s} value={s}>{s}</option>)}
    </select>
  )
}

function TextField({ label, value, onCommit, testId, disabled, placeholder }: {
  label: string; value?: string; onCommit: (v: string) => void; testId: string; disabled: boolean; placeholder?: string
}) {
  const [v, setV] = useState(value ?? '')
  useEffect(() => setV(value ?? ''), [value])
  return (
    <label className="field">
      <span>{label}</span>
      <input value={v} placeholder={placeholder} disabled={disabled} data-testid={testId}
        onChange={(e) => setV(e.target.value)} onBlur={() => v !== (value ?? '') && onCommit(v.trim())}
        onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
    </label>
  )
}

function DeadlineEditor({ deadline, onChange, disabled }: { deadline?: Deadline; onChange: (d: Deadline | undefined) => void; disabled: boolean }) {
  const mode = deadline?.business_days ? 'business' : deadline?.duration ? 'duration' : 'none'
  const [days, setDays] = useState(String(deadline?.business_days ?? 2))
  useEffect(() => setDays(String(deadline?.business_days ?? 2)), [deadline?.business_days])
  return (
    <fieldset className="deadline">
      <legend>Prazo</legend>
      <select value={mode} disabled={disabled} data-testid="deadline-mode" onChange={(e) => {
        const m = e.target.value
        onChange(m === 'business' ? { business_days: 2, cutoff: '23:59' } : m === 'duration' ? { duration: 'PT4H' } : undefined)
      }}>
        <option value="none">sem prazo</option>
        <option value="business">dias úteis (calendário da empresa)</option>
        <option value="duration">duração corrida</option>
      </select>
      {mode === 'business' ? (
        <div className="row">
          <label className="field small">
            <span>Dias úteis</span>
            <input type="number" min={1} max={250} value={days} disabled={disabled} data-testid="deadline-days"
              onChange={(e) => setDays(e.target.value)}
              onBlur={() => {
                const n = Number(days)
                if (Number.isInteger(n) && n >= 1 && n !== deadline?.business_days) onChange({ ...deadline, business_days: n, duration: undefined })
              }}
              onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
          </label>
          <TextField label="Corte" value={deadline?.cutoff} testId="deadline-cutoff" disabled={disabled} placeholder="23:59"
            onCommit={(v) => onChange({ ...deadline, cutoff: v || undefined })} />
        </div>
      ) : null}
      {mode === 'duration' ? (
        <TextField label="Duração (ISO, ex. PT4H, P2D)" value={deadline?.duration} testId="deadline-duration" disabled={disabled}
          onCommit={(v) => onChange({ duration: v })} />
      ) : null}
      <p className="hint">{describeDeadline(deadline)}</p>
    </fieldset>
  )
}

export function StepPanel({ definition, name, readOnly, onChange }: Props) {
  const step = definition.steps[name]
  if (!step) return null
  const steps = Object.keys(definition.steps).sort()
  const set = (patch: Partial<Step>) => onChange(updateStep(definition, name, patch))
  const list = (v: string) => v.split(',').map((x) => x.trim()).filter(Boolean)

  return (
    <div className="panel-body" data-testid="step-panel">
      <div className="panel-title">
        <span className="kind">{STEP_LABEL[step.type]}</span>
        <h3>{name}</h3>
        {definition.start === name ? <span className="pill">início</span> : null}
      </div>

      {step.type === 'human_task' ? (
        <>
          <TextField label="Papéis (separados por vírgula)" value={(step.roles ?? []).join(', ')} testId="field-roles" disabled={readOnly}
            onCommit={(v) => set({ roles: list(v) })} />
          <div className="field"><span>Saídas</span><div className="chips">{(step.outcomes ?? []).map((o) => <code key={o}>{o}</code>)}</div></div>
          <label className="field"><span>Depois de resolvida, vai para</span>
            <StepSelect value={step.next} steps={steps} testId="field-next" disabled={readOnly} onChange={(v) => set({ next: v })} /></label>
          <DeadlineEditor deadline={step.deadline} disabled={readOnly} onChange={(d) => set({ deadline: d, ...(d ? {} : { grace: undefined, on_deadline: undefined, late_response: undefined }) })} />
          {step.deadline ? (
            <>
              <TextField label="Tolerância (ISO, padrão PT5M)" value={step.grace} testId="field-grace" disabled={readOnly} placeholder="PT5M"
                onCommit={(v) => set({ grace: v })} />
              <label className="field"><span>Se o prazo vencer, vai para</span>
                <StepSelect value={step.on_deadline} steps={steps} testId="field-on-deadline" disabled={readOnly}
                  onChange={(v) => set({ on_deadline: v, late_response: 'record_only' })} /></label>
              <p className="hint">Resposta depois do prazo fica registrada para revisão (record_only).</p>
            </>
          ) : null}
        </>
      ) : null}

      {step.type === 'decision' ? (
        <div className="field"><span>Rotas por saída</span>
          {Object.entries(step.routes ?? {}).sort().map(([outcome, to]) => (
            <div className="route" key={outcome}>
              <code>{outcome}</code><span>→</span>
              <StepSelect value={to} steps={steps} testId={`route-${outcome}`} disabled={readOnly}
                onChange={(v) => set({ routes: { ...step.routes, [outcome]: v } })} />
            </div>
          ))}
        </div>
      ) : null}

      {step.type === 'external_command' ? (
        <>
          <TextField label="Tipo de comando (o CRM interpreta)" value={step.command_type} testId="field-command-type" disabled={readOnly}
            onCommit={(v) => set({ command_type: v })} />
          <TextField label="Espera pela resposta (ISO)" value={step.reply_timeout} testId="field-reply-timeout" disabled={readOnly}
            onCommit={(v) => set({ reply_timeout: v })} />
          {(['on_confirmed', 'on_rejected', 'on_no_reply'] as const).map((k) => (
            <label className="field" key={k}>
              <span>{k === 'on_confirmed' ? 'Confirmado → ' : k === 'on_rejected' ? 'Recusado → ' : 'Sem resposta → '}</span>
              <StepSelect value={step[k]} steps={steps} testId={`field-${k}`} disabled={readOnly} allowEmpty={k === 'on_no_reply'} onChange={(v) => set({ [k]: v })} />
            </label>
          ))}
          <label className="field">
            <span>Confirmar só com o identificador do CRM (result_ref)</span>
            <input type="checkbox" checked={!!step.require_result_ref} disabled={readOnly} data-testid="field-require-result-ref"
              onChange={(e) => set({ require_result_ref: e.target.checked || undefined })} />
          </label>
        </>
      ) : null}

      {step.type === 'wait_event' ? (
        <>
          <div className="field"><span>Eventos esperados</span>
            {Object.entries(step.on_events ?? {}).sort().map(([ev, to]) => (
              <div className="route" key={ev}><code>{ev}</code><span>→</span>
                <StepSelect value={to} steps={steps} testId={`event-${ev}`} disabled={readOnly}
                  onChange={(v) => set({ on_events: { ...step.on_events, [ev]: v } })} />
              </div>
            ))}
          </div>
          <DeadlineEditor deadline={step.deadline} disabled={readOnly} onChange={(d) => set({ deadline: d })} />
          {step.deadline ? (
            <label className="field"><span>Se o prazo vencer, vai para</span>
              <StepSelect value={step.on_deadline} steps={steps} testId="field-on-deadline" disabled={readOnly} onChange={(v) => set({ on_deadline: v })} /></label>
          ) : null}
        </>
      ) : null}

      {step.type === 'end' ? (
        <TextField label="Resultado" value={step.result} testId="field-result" disabled={readOnly} onCommit={(v) => set({ result: v })} />
      ) : null}

      {step.type !== 'decision' ? (
        <TextField label="Status de negócio (opaco, o CRM interpreta)" value={step.business_status} testId="field-business-status" disabled={readOnly}
          onCommit={(v) => set({ business_status: v })} />
      ) : null}
    </div>
  )
}
