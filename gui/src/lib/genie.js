// Ask Genie — the always-available quick question, shared by the floating
// button in the app shell and the Chat page that answers it.
//
// The iPhone app has the same affordance; its routing lives in
// ChatStore.genieAgent(for:). Keep the two in step: a gateway-provided
// "genie" agent always wins, otherwise the specialist whose id, name,
// description and tags overlap the question the most.

export const GENIE_SUGGESTIONS = [
  'What needs my attention?',
  'Summarize today’s activity',
  'What should I know right now?',
]

const ALIAS_GROUPS = [
  ['weather', 'forecast', 'temperature', 'rain', 'snow', 'storm', 'climate'],
  ['market', 'stock', 'equity', 'ticker', 'finance', 'investment', 'crypto'],
  ['travel', 'trip', 'flight', 'hotel', 'vacation', 'destination'],
  ['technology', 'tech', 'cio', 'software', 'security', 'cloud', 'architecture'],
]

export function routingWords(text) {
  return new Set(
    String(text || '')
      .toLowerCase()
      .split(/[^\p{L}\p{N}]+/u)
      .filter(w => w.length > 2),
  )
}

function withAliases(words) {
  const out = new Set(words)
  for (const group of ALIAS_GROUPS) {
    if (group.some(w => words.has(w))) group.forEach(w => out.add(w))
  }
  return out
}

function intersectionSize(a, b) {
  let n = 0
  for (const w of a) if (b.has(w)) n++
  return n
}

export function genieScore(agent, queryWords) {
  const identity = `${agent.id || ''} ${agent.name || ''}`.toLowerCase().split(/[^\p{L}\p{N}]+/u)
  if (identity.includes('genie')) return 10_000
  const tags = Array.isArray(agent.tags) ? agent.tags.join(' ') : ''
  const agentWords = routingWords(`${agent.id || ''} ${agent.name || ''} ${agent.description || ''} ${tags}`)
  return intersectionSize(queryWords, agentWords) * 20
    + intersectionSize(withAliases(queryWords), withAliases(agentWords)) * 35
}

/** The agent that should answer `question`, or null when none can. `agents`
    is the Chat page's already-filtered list (enabled and chat-eligible). */
export function pickGenieAgent(agents, question) {
  const candidates = (agents || []).filter(a => a && a.id)
  if (!candidates.length) return null
  const queryWords = routingWords(question)
  let best = null
  let bestScore = -1
  for (const agent of candidates) {
    const score = genieScore(agent, queryWords)
    if (score > bestScore) { best = agent; bestScore = score }
  }
  return best
}

/** Builds the handoff the floating button stores for the Chat page. */
export function genieRequest(text) {
  const trimmed = String(text || '').trim()
  if (!trimmed) return null
  return { text: trimmed, at: Date.now() }
}
