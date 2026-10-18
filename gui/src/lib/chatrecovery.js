// Recovery for synchronous chat requests whose browser connection disappears
// while the gateway deliberately continues the run in the background.

const TRANSPORT_FAILURE = /(load failed|failed to fetch|network\s*error|network request failed|fetch failed|connection (?:was )?(?:lost|closed|reset))/i

export function isChatTransportError(error) {
  if (!error || error.status) return false
  // WebKit sometimes rejects a disconnected fetch with a native TypeError
  // whose message is the empty string. It is still a transport failure; an
  // HTTP/API failure reaches us with a status or a regular Error message.
  if (error instanceof TypeError && !String(error.message || '').trim()) return true
  return TRANSPORT_FAILURE.test(String(error.message || error))
}

export function detachedChatOutcome(entries, events, { agentId = '', sessionId = '', sentText = '', startedAt = 0, baselineEntryIds = null } = {}) {
  const history = Array.isArray(entries) ? entries : []
  const excluded = baselineEntryIds == null ? null : new Set(baselineEntryIds)
  // A terminal run event is authoritative. Tool/provider errors can recover,
  // and another conversation's events must never terminate this one.
  const terminal = (Array.isArray(events) ? events : [])
    .filter(event => event?.type === 'run.completed'
      && (!agentId || event.agent_id === agentId)
      && (!sessionId || event.session_id === sessionId)
      && (!startedAt || Date.parse(event.timestamp || '') >= startedAt - 2000))
    .sort((a, b) => Date.parse(b.timestamp || '') - Date.parse(a.timestamp || ''))[0]
  const payload = terminal?.payload || {}
  const metadata = payload.degraded || payload.success === false
    ? { ...(payload.metadata || {}), reasoning_degraded: 'true', outcome: payload.outcome || 'incomplete', outcome_summary: payload.summary || payload.error || 'This run did not finish successfully.' }
    : (payload.metadata || {})
  for (let i = history.length - 1; i >= 0; i--) {
    const entry = history[i]
    if (entry?.role !== 'user' || (agentId && entry.agent_id && entry.agent_id !== agentId)) continue
    if (sessionId && entry.session_id && entry.session_id !== sessionId) continue
    if (excluded?.has(entry.id)) continue
    // When available, the pre-send history boundary also handles clock skew.
    const at = Date.parse(entry.created_at || '')
    if (!excluded && startedAt && Number.isFinite(at) && at < startedAt - 2000) continue
    if (sentText && String(entry.content || '').trim() !== String(sentText).trim()) continue
    let answer = null
    for (let j = i + 1; j < history.length; j++) {
      if (history[j]?.role === 'user') break
      if (history[j]?.role === 'assistant' && (!agentId || !history[j].agent_id || history[j].agent_id === agentId)) { answer = history[j]; break }
    }
    if (answer && !excluded?.has(answer.id)) return { status: 'success', reply: answer.content || '', entry: answer, metadata: { ...(answer.metadata || {}), ...metadata } }
    break
  }
  if (terminal && payload.success === false) return { status: 'error', error: payload.summary || payload.error || 'The server-side run did not finish successfully.' }
  return { status: 'pending' }
}

export async function recoverDetachedChat({
  loadHistory,
  loadEvents,
  agentId,
  sentText,
  startedAt,
  sessionId,
  baselineEntryIds,
  timeoutMs = 48 * 60 * 60 * 1000,
  pollMs = 2500,
  now = () => Date.now(),
  sleep = ms => new Promise(resolve => setTimeout(resolve, ms)),
}) {
  const deadline = now() + timeoutMs
  do {
    const [historyResult, eventsResult] = await Promise.allSettled([loadHistory(), loadEvents()])
    const entries = historyResult.status === 'fulfilled' ? (historyResult.value?.entries || []) : []
    const events = eventsResult.status === 'fulfilled' ? (eventsResult.value?.events || []) : []
    const outcome = detachedChatOutcome(entries, events, { agentId, sessionId, sentText, startedAt, baselineEntryIds })
    if (outcome.status !== 'pending') return outcome
    if (now() >= deadline) break
    await sleep(Math.min(pollMs, Math.max(0, deadline - now())))
  } while (now() <= deadline)
  return { status: 'timeout' }
}
