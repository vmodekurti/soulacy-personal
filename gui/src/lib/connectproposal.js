// "Connect by asking": when the assistant wants to connect something, the
// gateway stops and asks. In the chat-first shell that question is one line in
// the conversation rather than a dialog over it. This file decides which
// requests get that treatment and how the line is worded.
//
// It only words what the request actually says. A connector made this way
// reads public pages and uses no sign-in, so that is all the line claims.

const CONNECT_TOOLS = new Set(['create_connector'])

/** True when a confirmation request is a "connect this" proposal. */
export function isConnectRequest(req) {
  return !!req && CONNECT_TOOLS.has(String(req.tool || ''))
}

/** "https://www.notion.so/product" → "notion.so"; a bare name is kept as typed. */
export function siteLabel(site) {
  const raw = String(site || '').trim()
  if (!raw) return ''
  const candidate = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw) ? raw : (/^[^\s/]+\.[^\s/]+/.test(raw) ? `https://${raw}` : '')
  if (candidate) {
    try { return new URL(candidate).hostname.replace(/^www\./i, '').toLowerCase() } catch { /* fall through */ }
  }
  return raw
}

function listOf(items) {
  if (items.length <= 1) return items.join('')
  if (items.length === 2) return `${items[0]} and ${items[1]}`
  return `${items.slice(0, -1).join(', ')}, and ${items[items.length - 1]}`
}

/**
 * The words for one connect proposal.
 * Returns null when the request is not a connect proposal.
 */
export function connectProposal(req) {
  if (!isConnectRequest(req)) return null
  const args = req.args || {}
  const sites = [...new Set((Array.isArray(args.sites) ? args.sites : []).map(siteLabel).filter(Boolean))]
  const name = String(args.name || '').trim()
  const shown = sites.slice(0, 3)
  const extra = sites.length - shown.length
  const where = sites.length
    ? listOf(extra > 0 ? [...shown, `${extra} more`] : shown)
    : 'the sites it suggested'
  return {
    callId: req.call_id || '',
    title: name ? `Connect ${name}` : (sites.length === 1 ? `Connect ${sites[0]}` : 'Connect these sites'),
    text: `Read public pages on ${where}.`,
    note: 'No sign-in is used and nothing is changed there.',
    intent: String(args.intent || '').trim(),
    sites,
  }
}
