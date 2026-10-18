// Present execution state separately from the model's answer text.
export function responseNotice(metadata = {}) {
  metadata ||= {}
  if (String(metadata.reasoning_degraded).toLowerCase() === 'true' || ['incomplete', 'partial', 'failed', 'empty'].includes(metadata.outcome)) {
    return { tone: 'warning', title: 'Work is incomplete', detail: metadata.outcome_summary || 'The run stopped before it could finish. Review the result before relying on it.' }
  }
  if (metadata.outcome === 'unverified' || metadata.autopilot_verification === 'unknown') {
    return { tone: 'neutral', title: 'Result not independently verified', detail: metadata.outcome_summary || 'The agent returned a result, but no independent completion check confirmed it.' }
  }
  return null
}
export function executionLimitRows(limits) {
  if (!limits || !['max_turns', 'max_tokens', 'max_llm_calls'].every(k => Number.isSafeInteger(limits[k]) && limits[k] >= 0) || typeof limits.run_timeout !== 'string') return []
  const count = n => n === 0 ? 'Unlimited' : n.toLocaleString()
  return [
    ['Turns', count(limits.max_turns)],
    ['Total tokens', count(limits.max_tokens)],
    ['Model calls', count(limits.max_llm_calls)],
    ['Run time', limits.run_timeout === 'unlimited' ? 'Unlimited' : limits.run_timeout],
  ]
}
