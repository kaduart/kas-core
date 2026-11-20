// Mirrors internal/process/definition.go (kas.process/v1). The Studio never
// adds business rules: it edits the same JSON the engine validates.
export interface Deadline {
  duration?: string
  business_days?: number
  cutoff?: string
}

export type StepType = 'human_task' | 'decision' | 'external_command' | 'wait_event' | 'end'

export interface Step {
  type: StepType
  roles?: string[]
  outcomes?: string[]
  next?: string
  deadline?: Deadline
  grace?: string
  on_deadline?: string
  late_response?: string
  routes?: Record<string, string>
  command_type?: string
  reply_timeout?: string
  max_status_queries?: number
  on_confirmed?: string
  on_rejected?: string
  on_no_reply?: string
  on_events?: Record<string, string>
  result?: string
  business_status?: string
}

export interface LayoutNode {
  id: string
  x: number
  y: number
}

export interface Definition {
  schema: string
  process_key: string
  start: string
  calendar?: { zone?: string; holidays?: string[] }
  steps: Record<string, Step>
  interrupts?: Record<string, string>
  layout?: { nodes?: LayoutNode[] } & Record<string, unknown>
}

export interface DefinitionView {
  process_key: string
  version: number
  checksum: string
  published_by: string
  published_at: string
  versions: number[]
  definition: Definition
}

export interface TaskView {
  id: number
  instance_id: number
  step: string
  roles: string[]
  outcomes: string[]
  due_at?: string
  cutoff_at?: string
  status: string
  outcome?: string
}

export interface CommandView {
  command_key: string
  step: string
  command_type: string
  generation: number
  status: string
  attempt: number
  status_queries: number
}

export interface HistoryEntry {
  id: number
  recorded_at: string
  kind: string
  step?: string
  service_id?: string
  actor_id?: string
  role?: string
  detail: Record<string, unknown> | null
}

export interface InstanceView {
  id: number
  process_key: string
  version: number
  business_ref: string
  status: string
  current_step: string
  state_version: number
  result?: string
  tasks: TaskView[] | null
  commands: CommandView[] | null
  history: HistoryEntry[] | null
}

export interface InstanceSummary {
  id: number
  process_key: string
  version: number
  business_ref: string
  status: string
  current_step: string
  result?: string
  started_at: string
  updated_at: string
}
