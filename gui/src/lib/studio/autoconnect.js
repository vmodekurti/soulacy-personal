// autoconnect.js — smart connections for the Guided Studio Builder (Story 7).
// When a step is dropped or generated, wire it to the most sensible upstream
// step automatically, and explain in plain English when a connection is invalid.
// Pure & unit-tested.

import { stepLabel } from './blockmeta.js'

// Structural kinds never carry runtime data, so they aren't auto-connect
// sources/targets for the work plan.
const STRUCTURAL = new Set(['trigger', 'exit', 'branch'])

// autoConnectEdge decides which existing node should feed a newly-added node.
// Heuristic: the most recently-added non-structural node that isn't the new node
// and doesn't already point at it. Returns { from, to } or null when there's no
// sensible source (e.g. the new node is the first real step).
//
// nodes: the flow nodes BEFORE the new node was appended (array order = add
// order); newNode: the node just added; edges: existing edges.
export function autoConnectEdge(nodes, newNode, edges = []) {
  if (!newNode || STRUCTURAL.has(newNode.kind)) return null
  for (let i = nodes.length - 1; i >= 0; i--) {
    const cand = nodes[i]
    if (!cand || cand.id === newNode.id) continue
    if (STRUCTURAL.has(cand.kind)) continue
    // don't duplicate an existing edge
    if (edges.some((e) => e && e.from === cand.id && e.to === newNode.id)) continue
    return { from: cand.id, to: newNode.id }
  }
  return null
}

// explainConnection returns null when a connection from → to is valid, or a
// plain-English reason string when it isn't (Story 7 "invalid connections
// explained in plain English").
export function explainConnection(fromNode, toNode) {
  if (!fromNode || !toNode) return 'One end of this connection is missing.'
  if (fromNode.id === toNode.id) return "A step can't connect to itself."
  if (fromNode.kind === 'exit') return `“${stepLabel(fromNode)}” is a delivery step — nothing runs after it.`
  if (toNode.kind === 'trigger') return `“${stepLabel(toNode)}” is the trigger — nothing can run before it.`
  return null
}

// wouldCreateCycle reports whether adding from → to closes a loop (following the
// existing edges from `to` back to `from`). Used to keep auto-connect acyclic.
export function wouldCreateCycle(edges, from, to) {
  const adj = new Map()
  for (const e of edges || []) {
    if (!e) continue
    if (!adj.has(e.from)) adj.set(e.from, [])
    adj.get(e.from).push(e.to)
  }
  const seen = new Set()
  const stack = [to]
  while (stack.length) {
    const n = stack.pop()
    if (n === from) return true
    if (seen.has(n)) continue
    seen.add(n)
    for (const nxt of adj.get(n) || []) stack.push(nxt)
  }
  return false
}
