/*
 * runstate.js — derive per-node execution status from a finished autonomous
 * build, so the canvas can show vibrant-but-restrained semantic accents
 * (success / repaired / problem) rather than leaving the user to read a report.
 *
 * Pure and data-only: it reads the BuildReport (verdict + attempt transcript +
 * residual problems) and the final preflight (per-node blockers/warnings) and
 * returns { nodeId -> status }. status ∈ 'ok' | 'repaired' | 'problem' | 'idle'.
 */

// Matches a node reference the loop writes into problem/residual lines, e.g.
// `step "create_notebook": …` or `node "b": …`.
const NODE_REF = /(?:step|node)\s+"([^"]+)"/g

// nodeIdsFromLines extracts referenced node ids from an array of message lines.
export function nodeIdsFromLines(lines) {
  const ids = new Set()
  for (const line of lines || []) {
    const s = String(line == null ? '' : line)
    let m
    NODE_REF.lastIndex = 0
    while ((m = NODE_REF.exec(s))) ids.add(m[1])
  }
  return ids
}

// problemNodeIds collects the ids of nodes still implicated at the end of the
// build: preflight blockers, actionable (dependency) warnings, and any node
// named in the report's residual (unresolved) problems.
export function problemNodeIds({ report, preflight } = {}) {
  const ids = new Set()
  const pf = preflight || {}
  for (const i of pf.blockers || []) if (i && i.nodeId) ids.add(i.nodeId)
  for (const i of pf.warnings || []) if (i && i.kind === 'dependency' && i.nodeId) ids.add(i.nodeId)
  for (const id of nodeIdsFromLines((report && report.residual) || [])) ids.add(id)
  return ids
}

// computeRunState maps every node in the report's workflow to a status:
//   - 'problem'  — still implicated at the end (preflight/residual)
//   - 'ok'       — the build verified or validated clean and this node is clean
//   - 'repaired' — named in a repair attempt that changed the draft, build not green
//   - 'idle'     — no signal
export function computeRunState({ report, preflight } = {}) {
  const out = {}
  if (!report) return out
  const nodes =
    (report.workflow && report.workflow.flow && report.workflow.flow.nodes) || []
  const problems = problemNodeIds({ report, preflight })
  const repaired = new Set()
  for (const a of report.attempts || []) {
    if (a && a.changed) for (const id of nodeIdsFromLines(a.problems || [])) repaired.add(id)
  }
  const green = !!(report.verified || report.ok)
  for (const n of nodes) {
    const id = n && n.id
    if (!id) continue
    if (problems.has(id)) out[id] = 'problem'
    else if (green) out[id] = 'ok'
    else if (repaired.has(id)) out[id] = 'repaired'
    else out[id] = 'idle'
  }
  return out
}
