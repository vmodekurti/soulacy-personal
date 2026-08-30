const GENERIC = new Set([
  'www', 'com', 'org', 'net', 'login', 'signin', 'sign', 'subscription',
  'account', 'website', 'session', 'private', 'workspace',
])

const ALIASES = {
  hbr: ['harvard business review'],
  technologyreview: ['mit technology review', 'technology review'],
  gartner: ['gartner'],
}

function meaningfulTokens(value) {
  return String(value || '').toLowerCase().split(/[^a-z0-9]+/)
    .filter(token => token.length >= 3 && !GENERIC.has(token))
}

function hostLabels(connection) {
  const values = [...(connection.allowed_domains || [])]
  if (connection.base_url) {
    try { values.push(new URL(connection.base_url).hostname) } catch (_) { /* ignored */ }
  }
  return values.flatMap(meaningfulTokens)
}

export function connectionMatchesPrompt(connection, prompt) {
  if (!connection || String(connection.status || '').toLowerCase() !== 'ready') return false
  const haystack = String(prompt || '').toLowerCase()
  if (!haystack.trim()) return false
  const tokens = new Set([...meaningfulTokens(connection.name), ...hostLabels(connection)])
  for (const token of tokens) {
    if (haystack.includes(token)) return true
    if ((ALIASES[token] || []).some(alias => haystack.includes(alias))) return true
  }
  return false
}

export function suggestedConnectionIDs(connections, prompt, canChoose = () => true) {
  return (connections || [])
    .filter(connection => canChoose(connection) && connectionMatchesPrompt(connection, prompt))
    .map(connection => connection.id)
    .filter(Boolean)
}

export function attachAuthenticatedSourceGuidance(systemPrompt, connections, selectedIDs) {
  const selected = (connections || []).filter(connection => selectedIDs.includes(connection.id))
  if (!selected.length) return String(systemPrompt || '')
  const heading = '## Authenticated Sources'
  const requestedHeading = '## Requested Outcome and Scope'
  const withoutPrior = String(systemPrompt || '').split(heading)[0].trim()
  const outcomeAt = withoutPrior.indexOf(requestedHeading)
  const base = (outcomeAt >= 0 ? withoutPrior.slice(0, outcomeAt) : withoutPrior).trim()
  const outcome = outcomeAt >= 0 ? withoutPrior.slice(outcomeAt).trim() : ''
  const lines = selected.map(connection => {
    const domains = (connection.allowed_domains || []).join(', ') || connection.base_url || 'its approved domains'
    return `- For ${domains}, use authenticated_fetch with connection ${connection.id}. Prefer the authenticated source over public web search when the request targets it.`
  })
  return `${base}\n\n${heading}\n${lines.join('\n')}${outcome ? `\n\n${outcome}` : ''}`.trim()
}
