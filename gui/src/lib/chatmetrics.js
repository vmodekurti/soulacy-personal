// Per-reply token/cost deltas for the Chat Tester (Story 9).
// Deltas are computed client-side by diffing the session-cumulative metrics
// (Story 7's /runs/:session_id/metrics) before and after each turn.

import { fmtTokens, fmtCost } from './metrics.js'

/**
 * Diffs two session-metric snapshots. prev may be null (first turn);
 * returns null when curr is unavailable. Negative deltas (snapshot reset,
 * clock skew) clamp to zero so the UI never shows "-N tokens".
 */
export function deltaMetrics(prev, curr) {
  if (!curr) return null
  const p = prev || {}
  const nz = (a, b) => Math.max(0, (a || 0) - (b || 0))
  return {
    tokens:   nz(curr.total_tokens, p.total_tokens),
    prompt:   nz(curr.prompt_tokens, p.prompt_tokens),
    comp:     nz(curr.comp_tokens, p.comp_tokens),
    cost:     nz(curr.cost_usd, p.cost_usd),
    llmCalls: nz(curr.llm_calls, p.llm_calls),
    model:    curr.model || '',
    provider: curr.provider || '',
    cumulative: curr,
  }
}

/**
 * Subtle one-line indicator: "+350 tok · $0.0035 · gpt-4o".
 * Empty string when there's nothing meaningful to show.
 */
export function deltaLabel(d) {
  if (!d || !d.tokens) return ''
  const parts = [`+${fmtTokens(d.tokens)} tok`]
  if (d.cost > 0) parts.push(fmtCost(d.cost))
  if (d.model) parts.push(d.model)
  return parts.join(' · ')
}

/** Tooltip with the full picture, shown on hover of the indicator. */
export function deltaTitle(d) {
  if (!d) return ''
  const c = d.cumulative || {}
  return [
    `this turn: ${d.prompt}↑ ${d.comp}↓ tokens` + (d.llmCalls > 1 ? ` over ${d.llmCalls} LLM calls` : ''),
    `session total: ${c.total_tokens ?? 0} tokens · ${fmtCost(c.cost_usd ?? 0)}`,
    d.provider ? `provider: ${d.provider}` : '',
  ].filter(Boolean).join('\n')
}
