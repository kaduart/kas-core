import { useEffect, useMemo, useState } from 'react'
import {
  Background, Controls, Handle, MarkerType, Position, ReactFlow, applyNodeChanges,
  type Edge, type Node, type NodeChange, type NodeProps,
} from '@xyflow/react'
import { describeDeadline, edgesOf, positionsOf, STEP_LABEL, type EdgeKind } from './model'
import type { Definition, Step } from './types'

export interface StepNodeData extends Record<string, unknown> {
  name: string
  step: Step
  isStart: boolean
  current: boolean
  selected: boolean
}

type StepNode = Node<StepNodeData, 'step'>

function Icon({ type }: { type: Step['type'] }) {
  const common = { width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  if (type === 'human_task')
    return <svg {...common}><circle cx="12" cy="8" r="4" /><path d="M4 21c0-4 4-6 8-6s8 2 8 6" /></svg>
  if (type === 'external_command')
    return <svg {...common}><path d="M4 12h12" /><path d="M12 6l6 6-6 6" /><path d="M20 4v16" /></svg>
  if (type === 'wait_event')
    return <svg {...common}><rect x="3" y="6" width="18" height="12" rx="1" /><path d="M3 7l9 6 9-6" /></svg>
  return null
}

function StepCard({ data }: NodeProps<StepNode>) {
  const { name, step, isStart, current, selected } = data
  const cls = ['node', `node-${step.type}`, current ? 'is-current' : '', selected ? 'is-selected' : ''].join(' ')
  const handles = (
    <>
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </>
  )
  if (step.type === 'decision') {
    return (
      <div className={cls} data-testid={`node-${name}`}>
        {handles}
        <div className="diamond"><span>✕</span></div>
        <div className="node-caption">{name}</div>
      </div>
    )
  }
  if (step.type === 'end') {
    return (
      <div className={cls} data-testid={`node-${name}`}>
        {handles}
        <div className="end-circle" />
        <div className="node-caption">
          {name}
          {step.result ? <small>resultado: {step.result}</small> : null}
        </div>
      </div>
    )
  }
  const sub =
    step.type === 'human_task'
      ? (step.roles ?? []).join(', ')
      : step.type === 'external_command'
        ? step.command_type
        : Object.keys(step.on_events ?? {}).join(', ')
  return (
    <div className={cls} data-testid={`node-${name}`}>
      {handles}
      {isStart ? <span className="start-badge">início</span> : null}
      <div className="node-head">
        <Icon type={step.type} />
        <span className="node-kind">{STEP_LABEL[step.type]}</span>
      </div>
      <div className="node-title">{name}</div>
      {sub ? <div className="node-sub">{sub}</div> : null}
      {step.deadline ? <div className="node-deadline" data-testid={`deadline-${name}`}>⏱ {describeDeadline(step.deadline)}</div> : null}
      {step.business_status ? <div className="node-status">status {step.business_status}</div> : null}
    </div>
  )
}

const nodeTypes = { step: StepCard }

const EDGE_STYLE: Record<EdgeKind, { stroke: string; dash?: string }> = {
  next: { stroke: '#5b6475' },
  route: { stroke: '#5b6475' },
  deadline: { stroke: '#c46a12', dash: '6 4' },
  confirmed: { stroke: '#1f8a4c' },
  rejected: { stroke: '#c0392b' },
  no_reply: { stroke: '#c46a12', dash: '6 4' },
  event: { stroke: '#2f6db5' },
  interrupt: { stroke: '#8e44ad', dash: '2 4' },
}

export interface FlowCanvasProps {
  definition: Definition
  current?: string
  selected?: string
  editable?: boolean
  onSelect?: (step: string | undefined) => void
  onMove?: (positions: Record<string, { x: number; y: number }>) => void
}

export function FlowCanvas({ definition, current, selected, editable, onSelect, onMove }: FlowCanvasProps) {
  const positions = useMemo(() => positionsOf(definition), [definition])
  const build = (): StepNode[] =>
    Object.entries(definition.steps).map(([name, step]) => ({
      id: name,
      type: 'step',
      position: positions[name],
      draggable: !!editable,
      data: { name, step, isStart: definition.start === name, current: current === name, selected: selected === name },
    }))
  const [nodes, setNodes] = useState<StepNode[]>(build)
  // Rebuild from the definition (the source of truth) and keep only what React
  // Flow measured, so a late dimension/select change can never bring back an
  // older version of the step data.
  useEffect(() => {
    setNodes((ns) => build().map((n) => {
      const old = ns.find((o) => o.id === n.id)
      return old?.measured ? { ...n, measured: old.measured } : n
    }))
  }, [definition, current, selected]) // eslint-disable-line react-hooks/exhaustive-deps

  const edges: Edge[] = useMemo(
    () =>
      edgesOf(definition).map((e) => {
        const st = EDGE_STYLE[e.kind]
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          label: e.label || undefined,
          type: 'smoothstep',
          markerEnd: { type: MarkerType.ArrowClosed, color: st.stroke },
          style: { stroke: st.stroke, strokeDasharray: st.dash, strokeWidth: 1.6 },
          labelStyle: { fontSize: 11, fill: st.stroke },
          labelBgStyle: { fill: 'var(--canvas)' },
        }
      }),
    [definition],
  )

  const onNodesChange = (changes: NodeChange<StepNode>[]) => setNodes((ns) => applyNodeChanges(changes, ns))
  const onNodeDragStop = (_: unknown, __: StepNode, dragged: StepNode[]) => {
    if (!editable || !onMove) return
    onMove({ ...positions, ...Object.fromEntries(dragged.map((n) => [n.id, n.position])) })
  }

  return (
    <div className="canvas" data-testid="canvas">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeDragStop={onNodeDragStop}
        onNodeClick={(_, n) => onSelect?.(n.id)}
        onPaneClick={() => onSelect?.(undefined)}
        nodesConnectable={false}
        fitView
        fitViewOptions={{ padding: 0.15 }}
        proOptions={{ hideAttribution: true }}
        minZoom={0.3}
      >
        <Background gap={20} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}
