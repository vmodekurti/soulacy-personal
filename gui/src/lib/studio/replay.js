/*
 * replay.js — turn a durable build trace into a scrubbable per-node timeline.
 *
 * The build trace (from /studio/build-trace) records, per attempt, a draft
 * snapshot (node ids), the preflight problem set, and each repair. This module
 * reconstructs an ordered list of FRAMES — one per attempt plus a final outcome
 * frame — each mapping every node to a status ('problem' | 'repaired' | 'ok' |
 * 'idle'). Stepping the frames lets the canvas REPLAY the build: nodes flip
 * red → amber → green as the loop diagnoses and fixes them, the "visceral read of
 * the system's heartbeat" applied to the repair loop itself.
 *
 * Pure and data-only, so the timeline is unit-testable independent of the canvas.
 */
import { nodeIdsFromLines } from './runstate.js'

// Union of node ids ever seen in snapshot events — the node universe to color.
function nodeUniverse(events) {
  const ids = new Set()
  for (const e of events || []) {
    const arr = e && e.data && e.data.node_ids
    if (Array.isArray(arr)) for (const id of arr) ids.add(id)
  }
  return ids
}

// attempt number -> Set(node ids implicated that attempt), from the preflight and
// repair events' problem lines.
function problemsByAttempt(events) {
  const map = new Map()
  for (const e of events || []) {
    if (!e || !e.attempt) continue
    if (e.kind !== 'preflight' && e.kind !== 'repair') continue
    const ids = nodeIdsFromLines((e.data && e.data.problems) || [])
    if (!ids.size) continue
    const set = map.get(e.attempt) || new Set()
    for (const id of ids) set.add(id)
    map.set(e.attempt, set)
  }
  return map
}

// Sorted unique attempt numbers seen anywhere in the trace (any event carrying a
// truthy attempt — attempt/snapshot/preflight/repair/verify), so a clean attempt
// with no problems still gets a frame.
function attemptNumbers(events, probs) {
  const set = new Set(probs.keys())
  for (const e of events || []) {
    if (e && e.attempt) set.add(e.attempt)
  }
  return [...set].sort((a, b) => a - b)
}

/*
 * buildReplayFrames(trace) → [{ index, attempt, label, byId }]. byId maps each
 * node to its status AT that point: implicated nodes are 'problem'; nodes that
 * were implicated the previous attempt but no longer are read 'repaired'; the
 * final frame reflects the verdict (all 'ok' on a green build, except any node
 * still implicated). Returns [] when there's nothing to replay.
 */
export function buildReplayFrames(trace) {
  const events = (trace && trace.events) || []
  const universe = [...nodeUniverse(events)]
  if (!universe.length) return []

  const probs = problemsByAttempt(events)
  const attempts = attemptNumbers(events, probs)

  const resultEv = events.find((e) => e && e.kind === 'result' && e.phase === 'done')
  const green = !!(resultEv && resultEv.data && (resultEv.data.verified || resultEv.data.ok))

  const frames = []
  let prev = new Set()
  for (const a of attempts) {
    const cur = probs.get(a) || new Set()
    const byId = {}
    for (const id of universe) {
      if (cur.has(id)) byId[id] = 'problem'
      else if (prev.has(id)) byId[id] = 'repaired'
      else byId[id] = 'idle'
    }
    frames.push({ index: frames.length, attempt: a, label: `Attempt ${a}`, byId })
    prev = cur
  }

  if (resultEv) {
    const last = attempts.length ? (probs.get(attempts[attempts.length - 1]) || new Set()) : new Set()
    const byId = {}
    for (const id of universe) {
      if (green) byId[id] = last.has(id) ? 'problem' : 'ok'
      else byId[id] = last.has(id) ? 'problem' : prev.has(id) ? 'repaired' : 'idle'
    }
    frames.push({ index: frames.length, attempt: 0, label: green ? 'Verified ✓' : 'Result', byId })
  }
  return frames
}
