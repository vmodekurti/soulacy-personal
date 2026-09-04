// blockmeta.js — plain-English labels, readiness, and risk for Studio blocks
// (Epic: Guided Studio Builder — slice items 3 "plain-English step labels",
// 5 "readiness/missing/risky states", 8 "domain-specific blocks").
//
// Pure, framework-free, and unit-tested. Operates on the raw FlowNode shape
// used in workflow.flow.nodes: { id, kind, tool, agent, code, description,
// input, output, inputs[], outputs[], params }.

// humanize turns a machine name (snake_case / kebab / dotted) into Title Case,
// e.g. "send_telegram_message" -> "Send Telegram Message".
export function humanize(s) {
  return String(s || '')
    .replace(/[_\-.]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/\b\w/g, (c) => c.toUpperCase())
}

// stepLabel returns a friendly, plain-English label for a block. Prefers an
// existing human description for python/tool nodes; otherwise derives a label
// from the tool/agent name or the node kind.
export function stepLabel(node) {
  if (!node) return 'Step'
  const kind = node.kind || ''
  const desc = (node.description || '').trim()
  switch (kind) {
    case 'trigger': return 'Trigger'
    case 'exit': {
      const route = node.params?.route
      return route ? `${humanize(route)} Delivery` : 'Delivery'
    }
    case 'branch': return desc || 'Decision'
    case 'python': return desc || 'Custom Python'
    case 'llm': return desc || 'LLM Extract'
    case 'agent': return desc || (node.agent ? `Run ${humanize(node.agent)}` : 'Run agent')
    case 'tool':
    default:
      return desc || (node.tool ? humanize(node.tool) : 'Step')
  }
}

// Tool/name fragments that indicate an external send (higher risk: leaves the
// system, contacts a third party, or posts publicly).
const EXTERNAL_SEND = /(send|post|email|mail|telegram|slack|discord|whatsapp|sms|tweet|publish|webhook|notify|deliver)/i
// Code fragments that indicate a Python step reaches out or touches the host.
const CODE_ELEVATED = /(subprocess|os\.system|socket|requests\.|urllib|http\.client|open\(|shutil|Popen)/

// riskLevel classifies a block as 'low' | 'medium' | 'high' so the UI can flag
// risky capabilities, external sends, and generated code (slice item 11).
export function riskLevel(node) {
  if (!node) return 'low'
  const kind = node.kind || ''
  const name = `${node.tool || ''} ${node.description || ''}`
  if (kind === 'tool' && EXTERNAL_SEND.test(name)) return 'high'
  if (kind === 'exit') {
    const route = node.params?.route
    return route && route !== 'console' ? 'medium' : 'low'
  }
  if (kind === 'python') {
    return CODE_ELEVATED.test(node.code || '') ? 'high' : 'medium'
  }
  if (kind === 'llm') return 'medium'
  if (kind === 'agent') return 'medium'
  return 'low'
}

// A python code body is "unwritten" when it's blank or still the TODO starter.
function pythonUnwritten(code) {
  const c = (code || '').trim()
  if (!c) return true
  // strip comment lines, then see if only a bare `return inputs`/pass remains
  const meaningful = c
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('#') && !l.startsWith('def run'))
  return meaningful.every((l) => l === 'return inputs' || l === 'pass' || l.startsWith('# TODO'))
}

// blockReadiness reports whether a block is ready, needs attention (missing a
// required field or not wired up), or is ready-but-risky. Returns
// { status: 'ready'|'needs-attention'|'risky', missing: [], reasons: [] }.
//
// ctx: { edges: FlowEdge[], entry: nodeId } — used for the connection check.
export function blockReadiness(node, ctx = {}) {
  const missing = []
  const reasons = []
  if (!node) return { status: 'needs-attention', missing: ['node'], reasons: ['Empty block'] }
  const kind = node.kind || ''
  const edges = ctx.edges || []
  const entry = ctx.entry || ''

  // Required fields per kind.
  if (kind === 'tool' && !node.tool) { missing.push('tool'); reasons.push('Pick which tool this step runs') }
  if (kind === 'agent' && !node.agent) { missing.push('agent'); reasons.push('Pick which agent this step calls') }
  if (kind === 'python' && pythonUnwritten(node.code)) { missing.push('code'); reasons.push('Add the Python code for this step') }
  if (kind === 'llm' && !((node.params && node.params.system) || '').trim()) { missing.push('system'); reasons.push('Describe what the LLM should extract or transform') }
  if (kind === 'exit' && !node.params?.route) { missing.push('route'); reasons.push('Choose where the result is delivered') }
  if (kind === 'trigger' && !node.params?.kind) { missing.push('trigger'); reasons.push('Choose what starts the flow') }

  // Connection check: a non-trigger, non-entry block that nothing flows into is
  // stranded. Structural triggers are entry points so they're exempt.
  if (kind !== 'trigger' && node.id && node.id !== entry) {
    const hasIncoming = edges.some((e) => e && e.to === node.id)
    const hasOutgoing = edges.some((e) => e && e.from === node.id)
    if (!hasIncoming && !hasOutgoing) reasons.push('Not connected to anything')
    else if (!hasIncoming) reasons.push('Nothing feeds into this step')
  }

  let status
  if (missing.length || reasons.some((r) => /not connected|feeds into/i.test(r))) {
    status = 'needs-attention'
  } else if (riskLevel(node) === 'high') {
    status = 'risky'
  } else {
    status = 'ready'
  }
  return { status, missing, reasons }
}

// needsAttention returns the subset of nodes that aren't ready yet, so the UI
// can group them under a "Needs attention" heading (slice item 7).
export function needsAttention(nodes, ctx = {}) {
  return (nodes || []).filter((n) => blockReadiness(n, ctx).status === 'needs-attention')
}
