export const feedScopes = ['home', 'results', 'unread', 'saved', 'archived']

export function resultID(sessionId, fallback) {
  const session = String(sessionId || '').trim()
  return session ? `result:${session}` : fallback
}

// A scheduled execution is commonly returned by both the run ledger and the
// phone delivery store. Session identity is the join key, so every client sees
// one result instead of two nearly identical cards. (#306)
export function buildFeedCards({ runs = [], deliveries = [], approvals = [] } = {}) {
  const attention = approvals.map(a => ({
    kind: 'approval', id: `approval:${a.call_id}`, agent: a.agent_id, at: a.created_at,
    tool: a.tool, args: a.args || {}, reason: a.reason || '', callId: a.call_id, session: a.session_id,
    legacyIds: [],
  }))
  const results = new Map()
  for (const r of runs) {
    if (!r.output || !r.output.trim()) continue
    const legacyId = `run:${r.id}`
    const id = resultID(r.sessionId, legacyId)
    results.set(id, {
      kind: 'run', id, agent: r.agentId, at: r.updatedAt || r.startedAt,
      title: '', body: r.output, ok: r.ok !== false && r.status !== 'failed', status: r.status,
      presentation: r.presentation || null, session: r.sessionId, trigger: r.trigger || '',
      steps: r.steps || 0, ms: r.durationMs || 0, legacyIds: [legacyId], deliveryId: '', unread: false,
    })
  }
  for (const d of deliveries) {
    const legacyId = `delivery:${d.id}`
    const id = resultID(d.session_id, legacyId)
    const run = results.get(id)
    results.set(id, {
      ...(run || {}), kind: run ? 'run' : 'delivery', id,
      agent: d.agent_id || run?.agent || '',
      at: new Date(d.created_at) > new Date(run?.at || 0) ? d.created_at : (run?.at || d.created_at),
      title: d.title || run?.title || '', body: d.body || run?.body || '',
      ok: run?.ok ?? true, status: run?.status || '', presentation: d.presentation || run?.presentation || null,
      session: d.session_id || run?.session || '', trigger: d.metadata?.trigger || run?.trigger || '',
      steps: run?.steps || 0, ms: run?.ms || 0, deliveryId: d.id, unread: !d.read_at,
      legacyIds: [...new Set([...(run?.legacyIds || []), legacyId])],
    })
  }
  const rank = c => c.kind === 'approval' ? 0 : 1
  return [...attention, ...results.values()].sort((a, b) => rank(a) - rank(b) || new Date(b.at) - new Date(a.at))
}

export function filterFeedCards(cards, scope, { read = new Set(), saved = new Set(), archived = new Set() } = {}) {
  const visible = (cards || []).filter((card) => {
    if (card.kind === 'approval') return scope === 'home'
    const isArchived = archived.has(card.id)
    if (scope === 'archived') return isArchived
    if (isArchived) return false
    if (scope === 'unread') return !read.has(card.id)
    if (scope === 'saved') return saved.has(card.id)
    if (scope === 'results') return true
    return scope === 'home' && !read.has(card.id)
  })
  return scope === 'home'
    ? [...visible.filter(c => c.kind === 'approval'), ...visible.filter(c => c.kind !== 'approval').slice(0, 5)]
    : visible
}

export function initialReadState(cards) {
  return new Set((cards || [])
    .filter(card => card.kind !== 'approval' && !card.unread)
    .map(card => card.id))
}

export function applyRemoteFeedState(cards, rows) {
  const aliases = new Map()
  for (const card of cards || []) {
    aliases.set(card.id, card.id)
    for (const legacyId of card.legacyIds || []) aliases.set(legacyId, card.id)
  }
  const read = initialReadState(cards)
  const saved = new Set()
  const archived = new Set()
  for (const row of rows || []) {
    if (!row?.card_id) continue
    const id = aliases.get(row.card_id) || row.card_id
    if (row.read) read.add(id); else read.delete(id)
    if (row.saved) saved.add(id)
    if (row.archived) archived.add(id)
  }
  return { read, saved, archived }
}

export function continuationPrompt(card, agentName) {
  const title = String(card?.title || '').trim()
  const body = String(card?.body || '').trim()
  const content = [title, body].filter(Boolean).join('\n\n').slice(0, 4000)
  return `Continue this ${card?.kind === 'delivery' ? 'result' : 'work'} from ${agentName || 'Soulacy'}:\n\n${content}`
}

export function groupResults(cards, agentName, read = new Set()) {
  const grouped = new Map()
  for (const card of cards || []) {
    if (card.kind === 'approval') continue
    const group = grouped.get(card.agent) || { id: card.agent, name: agentName(card.agent), cards: [], unread: 0 }
    group.cards.push(card)
    if (!read.has(card.id)) group.unread += 1
    grouped.set(card.agent, group)
  }
  return [...grouped.values()]
    .map(group => ({ ...group, cards: group.cards.sort((a, b) => new Date(b.at) - new Date(a.at)) }))
    .sort((a, b) => new Date(b.cards[0]?.at || 0) - new Date(a.cards[0]?.at || 0))
}
