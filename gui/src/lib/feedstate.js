export const feedScopes = ['feed', 'unread', 'saved', 'archived']

export function filterFeedCards(cards, scope, { read = new Set(), saved = new Set(), archived = new Set() } = {}) {
  return (cards || []).filter((card) => {
    if (card.kind === 'approval') return scope === 'feed'
    const isArchived = archived.has(card.id)
    if (scope === 'archived') return isArchived
    if (isArchived) return false
    if (scope === 'unread') return !read.has(card.id)
    if (scope === 'saved') return saved.has(card.id)
    return true
  })
}

export function initialReadState(cards) {
  return new Set((cards || [])
    .filter(card => card.kind !== 'approval' && !(card.kind === 'delivery' && card.unread))
    .map(card => card.id))
}

export function applyRemoteFeedState(cards, rows) {
  const read = initialReadState(cards)
  const saved = new Set()
  const archived = new Set()
  for (const row of rows || []) {
    if (!row?.card_id) continue
    if (row.read) read.add(row.card_id); else read.delete(row.card_id)
    if (row.saved) saved.add(row.card_id)
    if (row.archived) archived.add(row.card_id)
  }
  return { read, saved, archived }
}

export function continuationPrompt(card, agentName) {
  const title = String(card?.title || '').trim()
  const body = String(card?.body || '').trim()
  const content = [title, body].filter(Boolean).join('\n\n').slice(0, 4000)
  return `Continue this ${card?.kind === 'delivery' ? 'result' : 'work'} from ${agentName || 'Soulacy'}:\n\n${content}`
}
