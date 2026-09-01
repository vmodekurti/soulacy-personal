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

export function detachedChatOutcome(entries, events, { agentId = '', sentText = '', startedAt = 0 } = {}) {
  const history = Array.isArray(entries) ? entries : []
  // Successful runs persist the user and assistant entries together. Matching
  // the submitted text avoids relying on a mobile device's potentially skewed
  // clock and prevents an older answer in the same session being recovered.
  for (let i = history.length - 1; i >= 0; i--) {
    const entry = history[i]
    if (entry?.role !== 'user' || (agentId && entry.agent_id && entry.agent_id !== agentId)) continue
    if (sentText && String(entry.content || '').trim() !== String(sentText).trim()) continue
    const answer = history.slice(i + 1).find(candidate =>
      candidate?.role === 'assistant' && (!agentId || !candidate.agent_id || candidate.agent_id === agentId))
    if (answer) return { status: 'success', reply: answer.content || '', entry: answer }
    break
  }

  const terminal = (Array.isArray(events) ? events : [])
    .filter(event => {
      if (event?.type !== 'error') return false
      if (agentId && event.agent_id && event.agent_id !== agentId) return false
      const at = Date.parse(event.timestamp || '')
      return !startedAt || !Number.isFinite(at) || at >= startedAt - 2000
    })
    .sort((a, b) => Date.parse(b.timestamp || '') - Date.parse(a.timestamp || ''))[0]
  if (terminal) {
    const payload = terminal.payload || {}
    return { status: 'error', error: payload.error || payload.message || 'The server-side run failed.' }
  }
  return { status: 'pending' }
}

export async function recoverDetachedChat({
  loadHistory,
  loadEvents,
  agentId,
  sentText,
  startedAt,
  timeoutMs = 300000,
  pollMs = 2500,
  now = () => Date.now(),
  sleep = ms => new Promise(resolve => setTimeout(resolve, ms)),
}) {
  const deadline = now() + timeoutMs
  do {
    const [historyResult, eventsResult] = await Promise.allSettled([loadHistory(), loadEvents()])
    const entries = historyResult.status === 'fulfilled' ? (historyResult.value?.entries || []) : []
    const events = eventsResult.status === 'fulfilled' ? (eventsResult.value?.events || []) : []
    const outcome = detachedChatOutcome(entries, events, { agentId, sentText, startedAt })
    if (outcome.status !== 'pending') return outcome
    if (now() >= deadline) break
    await sleep(Math.min(pollMs, Math.max(0, deadline - now())))
  } while (now() <= deadline)
  return { status: 'timeout' }
}
