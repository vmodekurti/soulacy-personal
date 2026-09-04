/*
 * Map a compiled Studio workflow into @xyflow/svelte nodes & edges.
 *
 * Compile contract:
 *   workflow = {
 *     name, trigger:{type,config}, channels:[],
 *     flow:{ nodes:[{id,kind,tool,agent,input,output,
 *                    inputs:[{name,type,label}],outputs:[{name,type,label}],
 *                    params,x,y}],
 *            edges:[{from,to,if,from_port,to_port}], entry }
 *   }
 *
 * Mapping rules:
 *   - flow.nodes -> xyflow nodes. Position from node.x/node.y; when both are
 *     missing/zero, fall back to a simple left-to-right layered auto-layout
 *     (BFS depth from `entry` = column, ordinal-within-depth = row).
 *   - flow.edges -> xyflow edges (from->source, to->target). Edges whose `to`
 *     is "end"/"" are skipped (they only mark a terminal, not a real link).
 *     edge.from_port/to_port map to xyflow sourceHandle/targetHandle so they
 *     attach to the right declared port; edge.if becomes the edge LABEL and a
 *     conditional class; the fallback (no if) leg out of a branch is dashed
 *     ("else"). Each edge carries data.index = its ordinal in flow.edges so the
 *     Inspector can select it and write the `if` predicate back.
 *   - TYPED MULTI-HANDLES: when a node declares inputs[]/outputs[] we render one
 *     xyflow handle per declared port (handle id = port name). Nodes without
 *     declared ports keep a single default handle (legacy behaviour).
 *   - trigger renders as a dedicated START node feeding `entry`.
 *   - channels render as a dedicated OUTPUT node fed by terminal nodes
 *     (nodes with no outgoing real edge, or whose edge.to is end/"").
 *   - node colour/label keyed by kind (tool/agent/branch); agent reads as a
 *     peer-agent handoff, branch as a decision (diamond) node.
 *   - an optional `validation` ({errors[],warnings[]}) highlights offending
 *     nodes (red/amber ring) and edges (red stroke).
 */

import { blockReadiness } from './blockmeta.js'

const KIND_META = {
  tool:   { color: '#2bb3a3', label: 'Tool',   shape: 'card' },
  agent:  { color: '#6c63ff', label: 'Agent',  shape: 'peer' },
  branch: { color: '#f5a742', label: 'Branch', shape: 'decision' },
  python: { color: '#e06c9f', label: 'Python', shape: 'card' },
  llm:    { color: '#9b7cff', label: 'LLM',    shape: 'card' },
}

// Normalise a declared-port array ([{name,type,label}] | ["name"]) to a
// consistent [{name,type,label}] shape. Empty/absent -> [].
function normalisePorts(ports) {
  if (!Array.isArray(ports)) return []
  return ports
    .map((p) => {
      if (typeof p === 'string') return { name: p, type: '', label: p }
      if (!p || typeof p !== 'object') return null
      const name = p.name || p.id || ''
      if (!name) return null
      return { name, type: p.type || '', label: p.label || name }
    })
    .filter(Boolean)
}

const COL_W = 240
const ROW_H = 120
const X0 = 60
const Y0 = 140

const TERMINAL = new Set(['end', ''])

export function kindMeta(kind) {
  return KIND_META[kind] || { color: '#8b93ab', label: kind || 'node' }
}

function needsAutoLayout(nodes) {
  // If every node has the same (or zero) coordinates, lay out automatically.
  return nodes.every((n) => (!n.x && !n.y))
}

// BFS depth from entry → column index. Disconnected nodes get appended after
// the deepest reached column so nothing overlaps.
function layeredPositions(nodes, edges, entry) {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const adj = new Map(nodes.map((n) => [n.id, []]))
  edges.forEach((e) => {
    if (TERMINAL.has(e.to)) return
    if (adj.has(e.from) && byId.has(e.to)) adj.get(e.from).push(e.to)
  })

  const depth = new Map()
  const start = entry && byId.has(entry) ? entry : (nodes[0] && nodes[0].id)
  if (start) {
    const q = [start]
    depth.set(start, 0)
    while (q.length) {
      const cur = q.shift()
      for (const nx of adj.get(cur) || []) {
        if (!depth.has(nx)) {
          depth.set(nx, depth.get(cur) + 1)
          q.push(nx)
        }
      }
    }
  }

  let maxDepth = 0
  depth.forEach((d) => { if (d > maxDepth) maxDepth = d })
  // Place unreached nodes in trailing columns.
  nodes.forEach((n) => {
    if (!depth.has(n.id)) depth.set(n.id, ++maxDepth)
  })

  // Row index = order within a column.
  const rowOf = new Map()
  const counts = new Map()
  nodes.forEach((n) => {
    const d = depth.get(n.id)
    const r = counts.get(d) || 0
    rowOf.set(n.id, r)
    counts.set(d, r + 1)
  })

  const pos = new Map()
  nodes.forEach((n) => {
    pos.set(n.id, {
      x: X0 + depth.get(n.id) * COL_W,
      y: Y0 + rowOf.get(n.id) * ROW_H,
    })
  })
  return pos
}

// Build {nodeId->true} / {edgeIndex->true} highlight maps from a validation
// payload so node/edge mapping can flag offenders without rescanning per item.
function highlightMaps(validation) {
  const errNodes = new Set()
  const warnNodes = new Set()
  const errEdges = new Set()
  if (!validation || typeof validation !== 'object') {
    return { errNodes, warnNodes, errEdges }
  }
  const errs = Array.isArray(validation.errors) ? validation.errors : []
  const warns = Array.isArray(validation.warnings) ? validation.warnings : []
  errs.forEach((e) => {
    if (!e) return
    if (e.nodeId) errNodes.add(e.nodeId)
    if (Number.isInteger(e.edgeIndex)) errEdges.add(e.edgeIndex)
  })
  warns.forEach((w) => {
    if (w && w.nodeId) warnNodes.add(w.nodeId)
  })
  return { errNodes, warnNodes, errEdges }
}

/**
 * @param {object} workflow   the compiled draft workflow
 * @param {object} [validation] { errors[], warnings[] } to drive highlights
 * @returns {{ nodes: Array, edges: Array }} xyflow-ready arrays.
 */
export function toFlow(workflow, validation = null, runState = null) {
  const flow = (workflow && workflow.flow) || {}
  const rawNodes = Array.isArray(flow.nodes) ? flow.nodes : []
  const rawEdges = Array.isArray(flow.edges) ? flow.edges : []
  const entry = flow.entry || (rawNodes[0] && rawNodes[0].id) || ''

  const auto = needsAutoLayout(rawNodes)
  const positions = auto ? layeredPositions(rawNodes, rawEdges, entry) : null
  const { errNodes, warnNodes, errEdges } = highlightMaps(validation)

  const nodes = rawNodes.map((n) => {
    const meta = kindMeta(n.kind)
    const position = auto
      ? (positions.get(n.id) || { x: X0, y: Y0 })
      : { x: n.x || 0, y: n.y || 0 }
    const inputs = normalisePorts(n.inputs)
    const outputs = normalisePorts(n.outputs)
    return {
      id: n.id,
      type: 'studio',
      position,
      // class is consumed by NodeWrapper -> our :global() CSS for the ring.
      class: errNodes.has(n.id) ? 'studio-invalid'
        : (warnNodes.has(n.id) ? 'studio-warn' : undefined),
      data: {
        node: n,
        label: n.tool || n.agent || n.id,
        kindLabel: meta.label,
        kind: n.kind || 'node',
        shape: meta.shape,
        color: meta.color,
        description: n.description || '',
        inputs,
        outputs,
        isEntry: n.id === entry,
        invalid: errNodes.has(n.id),
        warn: warnNodes.has(n.id),
        // Readiness accent (Guided Studio Builder, Story 8): 'ready' |
        // 'needs-attention' | 'risky', so the canvas node shows the same status
        // dot as the Plan view.
        readiness: blockReadiness(n, { edges: rawEdges, entry }).status,
        // Post-build execution status ('ok'|'repaired'|'problem'|'idle') for the
        // node's semantic accent. Distinct from `invalid`/`warn` (author-time
        // validation): this reflects what the autonomous build actually did.
        runState: (runState && runState[n.id]) || 'idle',
      },
    }
  })

  const edges = []
  const ids = new Set(rawNodes.map((n) => n.id))
  const byId = new Map(rawNodes.map((n) => [n.id, n]))
  // Real flow edges (skip terminal markers). `i` is the ordinal in flow.edges
  // and is preserved on data.index so the Inspector can write `if` back and so
  // validation.errors[].edgeIndex can flag the right edge.
  rawEdges.forEach((e, i) => {
    if (TERMINAL.has(e.to)) return
    if (!ids.has(e.from) || !ids.has(e.to)) return
    const src = byId.get(e.from)
    const isBranch = src && src.kind === 'branch'
    const hasIf = !!(e.if && String(e.if).trim())
    const isElse = isBranch && !hasIf       // branch leg with no predicate = fallback
    const invalid = errEdges.has(i)
    const cls = [
      'studio-edge',
      hasIf ? 'cond' : '',
      isElse ? 'else' : '',
      invalid ? 'studio-invalid' : '',
    ].filter(Boolean).join(' ')
    edges.push({
      id: 'e-' + e.from + '-' + e.to + '-' + i,
      type: 'live',
      source: e.from,
      target: e.to,
      sourceHandle: e.from_port || e.fromPort || undefined,
      targetHandle: e.to_port || e.toPort || undefined,
      label: hasIf ? e.if : (isElse ? 'else' : undefined),
      animated: false,
      class: cls,
      // cond drives the conditional dash; active (set by the page while a build/
      // run is in flight) drives the flowing particles — the canvas heartbeat.
      data: { index: i, edge: e, cond: hasIf, active: false },
    })
  })

  // ── Trigger as a START node → entry ──────────────────────────────────────
  const trigger = (workflow && workflow.trigger) || null
  if (trigger && trigger.type && entry && ids.has(entry)) {
    const triggerY = (positions && positions.get(entry)) ? positions.get(entry).y
      : (nodes.find((x) => x.id === entry)?.position.y ?? Y0)
    nodes.unshift({
      id: '__trigger__',
      type: 'studioTrigger',
      position: { x: X0 - COL_W, y: triggerY },
      data: { label: trigger.type, config: trigger.config || {} },
      selectable: false,
    })
    edges.push({
      id: 'e-trigger-' + entry,
      source: '__trigger__',
      target: entry,
      animated: true,
    })
  }

  // ── OUTPUT node: the flow's result delivered to channels ─────────────────
  // Shown when channels are configured OR an explicit output node is set. The
  // result comes from the explicit output node when designated (drag a node →
  // output); otherwise from the terminal node(s) — no outgoing real edge — which
  // is the default.
  const channels = (workflow && Array.isArray(workflow.channels)) ? workflow.channels : []
  const explicitOutput = (flow.output && ids.has(flow.output)) ? flow.output : ''
  if (channels.length || explicitOutput) {
    let sources
    if (explicitOutput) {
      sources = rawNodes.filter((n) => n.id === explicitOutput)
    } else {
      const hasRealOut = new Set()
      rawEdges.forEach((e) => { if (!TERMINAL.has(e.to)) hasRealOut.add(e.from) })
      sources = rawNodes.filter((n) => !hasRealOut.has(n.id))
    }

    let maxX = X0
    nodes.forEach((nd) => { if (nd.position.x > maxX) maxX = nd.position.x })
    const avgY = sources.length
      ? sources.reduce((s, n) => {
          const nd = nodes.find((x) => x.id === n.id)
          return s + (nd ? nd.position.y : Y0)
        }, 0) / sources.length
      : Y0

    nodes.push({
      id: '__output__',
      type: 'studioOutput',
      position: { x: maxX + COL_W, y: avgY },
      data: { channels, explicit: !!explicitOutput },
      selectable: false,
    })
    sources.forEach((t, i) => {
      edges.push({
        id: 'e-' + t.id + '-output-' + i,
        source: t.id,
        target: '__output__',
        animated: true,
      })
    })
  }

  return { nodes, edges }
}
