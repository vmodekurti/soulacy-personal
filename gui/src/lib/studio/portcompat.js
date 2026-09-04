/*
 * portcompat.js — design-time type safety for typed port wires.
 *
 * Redesign principle #1: "type-safe wires, not magic strings. When a user
 * connects Node A to Node B, the editor guarantees that the output type of A
 * structurally satisfies the input type of B" — and an invalid connection should
 * be physically impossible to draw. This module is the pure decision function
 * behind that guarantee: given the graph and a proposed connection, it returns
 * whether the wire is type-valid and, if not, a short human reason.
 *
 * Philosophy: be permissive where the system is honestly untyped (most ports
 * carry no declared type today), strict where types are declared. `json` is the
 * universal structured type (the standard JSON handshake), so it satisfies and is
 * satisfied by anything. An empty/absent type or an explicit `any` is a wildcard.
 * This blocks the real mistakes (wiring a string into a number) without
 * obstructing the common untyped case.
 */

// Canonicalize a declared port type to a known token.
export function canonType(t) {
  const s = String(t || '').trim().toLowerCase()
  if (!s) return '' // untyped — wildcard
  switch (s) {
    case 'any':
    case '*':
      return 'any'
    case 'json':
    case 'object':
    case 'map':
    case 'dict':
      return 'json'
    case 'string':
    case 'str':
    case 'text':
      return 'string'
    case 'number':
    case 'int':
    case 'integer':
    case 'float':
    case 'double':
      return 'number'
    case 'bool':
    case 'boolean':
      return 'bool'
    case 'list':
    case 'array':
      return 'list'
    default:
      return s // unknown custom type: compared by exact name
  }
}

// Wildcards satisfy and are satisfied by anything.
function isWild(t) {
  return t === '' || t === 'any' || t === 'json'
}

/*
 * portsCompatible(outType, inType) — may a value of outType flow into inType?
 * Rules, in order:
 *   - either side a wildcard ('', 'any', 'json')           → compatible
 *   - identical canonical types                            → compatible
 *   - a few safe widenings (number/bool → string)          → compatible
 *   - everything else                                      → incompatible
 */
export function portsCompatible(outType, inType) {
  const o = canonType(outType)
  const i = canonType(inType)
  if (isWild(o) || isWild(i)) return true
  if (o === i) return true
  // Safe widenings: any scalar renders cleanly into a string sink.
  if (i === 'string' && (o === 'number' || o === 'bool')) return true
  return false
}

// Find a node's declared port type for a given handle (port name) and direction.
// Returns '' when the node, port, or type is absent (untyped → wildcard).
export function portType(node, handle, dir) {
  if (!node || !handle) return ''
  const list = dir === 'out' ? node.outputs : node.inputs
  if (!Array.isArray(list)) return ''
  for (const p of list) {
    const name = typeof p === 'string' ? p : p && (p.name || p.id)
    if (name === handle) {
      return typeof p === 'string' ? '' : (p && p.type) || ''
    }
  }
  return ''
}

// Index nodes by id for quick lookup.
function indexNodes(nodes) {
  const m = new Map()
  for (const n of nodes || []) if (n && n.id) m.set(n.id, n)
  return m
}

/*
 * validateConnection({ nodes, source, target, sourceHandle, targetHandle })
 * → { ok, reason }. The single design-time gate the canvas calls while a wire is
 * being dragged. Only enforces TYPE compatibility (structural graph rules —
 * self-loops, duplicates, framing nodes — are the caller's concern). When either
 * endpoint declares no port type, the connection is allowed (untyped wildcard).
 */
export function validateConnection({ nodes, source, target, sourceHandle, targetHandle }) {
  const byId = indexNodes(nodes)
  const src = byId.get(source)
  const tgt = byId.get(target)
  if (!src || !tgt) return { ok: true, reason: '' } // unknown nodes: not our concern
  const outT = portType(src, sourceHandle, 'out')
  const inT = portType(tgt, targetHandle, 'in')
  if (portsCompatible(outT, inT)) return { ok: true, reason: '' }
  return {
    ok: false,
    reason: `type mismatch: ${canonType(outT) || 'untyped'} → ${canonType(inT) || 'untyped'}`,
  }
}
