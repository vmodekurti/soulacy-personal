// flowgraph.js — layout + labels for E25 workflow graphs (read-only render
// on the Flow page).

/**
 * computeFlowLayout assigns each node a {col, row}: col = shortest-path
 * depth from the entry (BFS over forward edges, cycles ignored), row =
 * order within the column. Unreachable nodes land in a trailing column.
 */
export function computeFlowLayout(nodes, edges, entry) {
  const ids = nodes.map(n => n.id)
  const start = entry && ids.includes(entry) ? entry : ids[0]
  const out = new Map(ids.map(id => [id, []]))
  for (const e of edges || []) {
    if (out.has(e.from) && ids.includes(e.to)) out.get(e.from).push(e.to)
  }

  const col = new Map()
  if (start !== undefined) {
    col.set(start, 0)
    const q = [start]
    while (q.length) {
      const cur = q.shift()
      for (const nxt of out.get(cur) || []) {
        if (!col.has(nxt)) {
          col.set(nxt, col.get(cur) + 1)
          q.push(nxt)
        }
      }
    }
  }
  let maxCol = -1
  for (const c of col.values()) maxCol = Math.max(maxCol, c)
  for (const id of ids) {
    if (!col.has(id)) col.set(id, maxCol + 1) // unreachable → trailing col
  }

  const rowCounter = new Map()
  const layout = new Map()
  for (const id of ids) {
    const c = col.get(id)
    const r = rowCounter.get(c) || 0
    rowCounter.set(c, r + 1)
    layout.set(id, { col: c, row: r })
  }
  return layout
}

/** Human label for one flow edge: predicate + cycle budget. */
export function flowEdgeLabel(edge) {
  const parts = []
  if (edge.if) {
    let p = edge.if.trim()
    if (p.length > 28) p = p.slice(0, 28) + '…'
    parts.push(p)
  }
  if ((edge.max_iterations || 0) > 1) parts.push(`↺×${edge.max_iterations}`)
  return parts.join('  ')
}

/** Icon + title for one flow node. */
export function flowNodeLabel(node) {
  const kind = node.kind || (node.tool ? 'tool' : node.agent ? 'agent' : 'branch')
  if (kind === 'agent') return `🤝 ${node.id}\nagent: ${node.agent}`
  if (kind === 'branch') return `◇ ${node.id}`
  return `⚙ ${node.id}\n${node.tool || ''}`
}
