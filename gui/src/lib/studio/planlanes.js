// planlanes.js — organizes a Studio workflow into the six lanes of the Guided
// Studio Builder — Trigger, Gather, Think, Act, Verify, Deliver — and derives the
// plain-English "plan review" shown before save. Pure & unit-tested.
//
// The six lanes mirror how an automation actually runs, so a non-expert can read
// the plan top-to-bottom: what starts it (Trigger), what inputs it collects
// (Gather), how it reasons (Think), what it does (Act), how it self-checks
// (Verify), and where the result goes (Deliver).
//
// Consumes the workflow model used across Studio: { name, trigger, channels[],
// flow: { nodes[], edges[], entry, output } } and the block helpers.

import { stepLabel, blockReadiness, riskLevel } from './blockmeta.js'

// laneOf assigns a flow node to a top-level bucket. Triggers start;
// exits/deliveries finish; everything else is "work" (further split by
// workLaneOf into Gather/Think/Act/Verify).
export function laneOf(node) {
  const k = node?.kind || ''
  if (k === 'trigger') return 'trigger'
  if (k === 'exit') return 'delivery'
  return 'work'
}

// workLaneOf classifies a work node into one of the four middle lanes. It reads
// the node kind plus a haystack of its tool/description/id, using intent
// keywords. Heuristic but stable: Verify wins on explicit validation intent;
// Gather covers reads/fetches/lookups; Act covers side-effecting tools; Think
// covers reasoning/transform steps (python, agent, llm) and anything unclassified.
export function workLaneOf(node) {
  const k = node?.kind || ''
  const hay = `${node?.tool || ''} ${node?.description || node?.label || ''} ${node?.id || ''}`.toLowerCase()

  if (/(verify|validat|\bcheck(?:_|\b)|assert|ensure|guard|review|sanity|confirm|\btest(?:_|\b)|lint)/.test(hay)) return 'verify'

  // Gather: pulling inputs/context in. Checked before Act so read-ish tools
  // (e.g. a screener that "runs" but really collects candidates) land here.
  if (/(fetch|http_get|\bget_|read|read_file|search|screen|screener|list|load|query|lookup|retrieve|scrape|browse|knowledge|\brag\b|price|weather|download|pull)/.test(hay)) return 'gather'

  // Act: side effects / outputs. Applies to tools that do something external.
  if (k === 'tool' && /(send|post|write|create|update|delete|publish|deliver|notify|email|message|upload|execute|\brun_|dispatch|submit)/.test(hay)) return 'act'

  // Think: reasoning / transformation.
  if (k === 'python' || k === 'agent' || k === 'llm' || k === 'reason' || k === 'reasoning') return 'think'

  // Remaining tools do something → Act; anything else → Think.
  if (k === 'tool') return 'act'
  return 'think'
}

// Order the work nodes by following edges from the entry (topological-ish), so
// the plan reads start-to-finish. Unreachable nodes are appended in array order.
function orderWork(nodes, edges, entry) {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const out = []
  const seen = new Set()
  const visit = (id) => {
    const n = byId.get(id)
    if (!n || seen.has(id)) return
    seen.add(id)
    if (laneOf(n) === 'work') out.push(n)
    edges.filter((e) => e && e.from === id).forEach((e) => visit(e.to))
  }
  if (entry) visit(entry)
  for (const n of nodes) if (laneOf(n) === 'work' && !seen.has(n.id)) { seen.add(n.id); out.push(n) }
  return out
}

// buildCards returns { trigger, work, delivery } — arrays of "lane cards". Each
// card carries the node plus its computed label, readiness, and risk so the view
// can render without recomputing. `work` is edge-ordered (start-to-finish).
// Delivery also includes synthetic channel cards. This is the shared internal
// model; toLanes() further splits `work` into the four middle lanes, while
// planReview() consumes the flat ordered `work` to preserve execution order.
function buildCards(workflow) {
  const flow = workflow?.flow || {}
  const nodes = flow.nodes || []
  const edges = flow.edges || []
  const ctx = { edges, entry: flow.entry || '' }
  const card = (node) => ({
    id: node.id,
    node,
    label: stepLabel(node),
    readiness: blockReadiness(node, ctx),
    risk: riskLevel(node),
  })

  const trigger = nodes.filter((n) => laneOf(n) === 'trigger').map(card)
  const work = orderWork(nodes, edges, ctx.entry).map(card)
  const delivery = nodes.filter((n) => laneOf(n) === 'delivery').map(card)

  // A workflow.trigger with no explicit trigger node still belongs in the lane
  // as an implied "how it starts" card.
  if (trigger.length === 0 && workflow?.trigger?.type) {
    trigger.push({
      id: '__trigger__', node: null, implied: true,
      label: triggerLabel(workflow.trigger),
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'low',
    })
  }
  // Channels are delivery destinations even without an exit node.
  for (const ch of workflow?.channels || []) {
    delivery.push({
      id: `__chan__${ch}`, node: null, implied: true,
      label: `Send to ${ch}`,
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'medium',
    })
  }
  if (delivery.length === 0) {
    delivery.push({
      id: '__console__', node: null, implied: true, label: 'Return the result',
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'low',
    })
  }
  return { trigger, work, delivery }
}

// LANES is the canonical ordered list of lane keys/labels for rendering.
export const LANES = [
  { key: 'trigger', label: 'Trigger' },
  { key: 'gather', label: 'Gather' },
  { key: 'think', label: 'Think' },
  { key: 'act', label: 'Act' },
  { key: 'verify', label: 'Verify' },
  { key: 'deliver', label: 'Deliver' },
]

// toLanes returns the six lanes { trigger, gather, think, act, verify, deliver },
// each an array of lane cards. The middle four are the ordered work nodes bucketed
// by workLaneOf, preserving execution order within each lane.
export function toLanes(workflow) {
  const { trigger, work, delivery } = buildCards(workflow)
  const gather = [], think = [], act = [], verify = []
  const buckets = { gather, think, act, verify }
  for (const c of work) {
    const wl = c.node ? workLaneOf(c.node) : 'think'
    ;(buckets[wl] || think).push(c)
  }
  return { trigger, gather, think, act, verify, deliver: delivery }
}

// migrateEndpoints converts legacy structural trigger/exit NODES into the
// workflow's trigger + channels settings and removes them from the graph, so
// old flows authored with draggable entry/exit blocks keep working under the
// lane model (Guided Studio Builder, Story 2). Non-destructive: returns a new
// workflow and only migrates when there's something to migrate.
export function migrateEndpoints(workflow) {
  const flow = workflow?.flow
  if (!flow || !Array.isArray(flow.nodes)) return workflow
  const triggerNodes = flow.nodes.filter((n) => n.kind === 'trigger')
  const exitNodes = flow.nodes.filter((n) => n.kind === 'exit')
  if (triggerNodes.length === 0 && exitNodes.length === 0) return workflow

  let trigger = workflow.trigger || null
  let entry = flow.entry || ''
  const channels = Array.isArray(workflow.channels) ? [...workflow.channels] : []
  const removeIds = new Set()

  // First trigger node → workflow.trigger; entry becomes whatever it fed.
  if (triggerNodes.length && !trigger?.type) {
    const t = triggerNodes[0]
    const p = t.params || {}
    trigger = { type: p.kind === 'cron' ? 'schedule' : (p.kind || 'manual'), config: p.config || {} }
    const fed = (flow.edges || []).find((e) => e && e.from === t.id)
    if (fed) entry = fed.to
    removeIds.add(t.id)
  }
  // Exit nodes → a delivery channel (when the route names one) and removed.
  // ONLY channel exits migrate. An http/console exit carries configuration
  // (e.g. a webhook URL) that has no home on the workflow object, and the
  // backend reads it straight off the graph (studio.DeriveEndpoints). Removing
  // those here silently deleted a configured endpoint every time the draft was
  // reopened, imported, auto-fixed or healed.
  for (const x of exitNodes) {
    const p = x.params || {}
    const route = p.route || ''
    if (route && route !== 'channel') continue
    const chan = p.config?.channel || p.config?.id
    if (route === 'channel' && chan && !channels.includes(chan)) channels.push(chan)
    removeIds.add(x.id)
  }

  const nodes = flow.nodes.filter((n) => !removeIds.has(n.id))
  const edges = (flow.edges || []).filter((e) => e && !removeIds.has(e.from) && !removeIds.has(e.to))
  return {
    ...workflow,
    trigger: trigger || workflow.trigger,
    channels,
    flow: { ...flow, nodes, edges, entry },
  }
}

// triggerLabel renders a workflow.trigger into plain English.
export function triggerLabel(trigger) {
  if (!trigger) return 'Manual'
  const t = trigger.type || 'manual'
  if (t === 'schedule' || t === 'cron') {
    const cron = trigger.config?.cron
    return cron ? `On a schedule (${cron})` : 'On a schedule'
  }
  if (t === 'http') return 'On an HTTP request'
  if (t === 'channel') return 'When a message arrives'
  return 'Run manually'
}

// planReview builds the plain-English summary shown before save (slice items
// 6 & 11): when it runs, what it does, what tools/code it uses, where output
// goes, and what needs attention or is risky.
export function planReview(workflow) {
  const { trigger, work, delivery } = buildCards(workflow)
  const usesTools = []
  const usesCode = []
  const risks = []
  const attention = []
  const perms = new Set()

  const SYSTEMISH = /(shell|exec|file|read_file|write_file|fs_|filesystem|os_|system)/i
  const WEBISH = /(http|fetch|url|request|api|browse|search)/i

  for (const c of work) {
    if (c.node?.kind === 'tool' && c.node.tool) {
      usesTools.push(c.label)
      if (SYSTEMISH.test(c.node.tool)) perms.add('Access files or the system')
      if (WEBISH.test(c.node.tool)) perms.add('Make web requests')
    }
    if (c.node?.kind === 'python') { usesCode.push(c.label); perms.add('Run custom code') }
    if (c.node?.kind === 'agent') perms.add('Delegate to another agent')
    if (c.risk === 'high') { risks.push(`${c.label} (external/elevated)`); perms.add('Send data externally') }
    if (c.readiness.status === 'needs-attention') attention.push({ label: c.label, reasons: c.readiness.reasons })
  }
  for (const c of delivery) {
    if (c.risk === 'high' || (c.node?.kind === 'exit' && c.risk !== 'low')) {
      risks.push(`${c.label} (sends externally)`)
      perms.add('Send messages externally')
    }
  }

  return {
    when: trigger[0]?.label || 'Manual',
    does: work.map((c) => c.label),
    usesTools,
    usesCode,
    deliversTo: delivery.map((c) => c.label),
    permissions: Array.from(perms),
    risks,
    attention,
    ready: attention.length === 0,
  }
}
