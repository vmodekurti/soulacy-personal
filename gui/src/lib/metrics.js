// Formatting helpers for run-level metrics (Story 7). Shared by the
// RunMetrics component and tested in metrics.test.js.

/** 1234 → "1.2k", 999 → "999", 2500000 → "2.5M". */
export function fmtTokens(n) {
  if (n == null || isNaN(n)) return '–'
  if (n >= 1_000_000) return trimZero(n / 1_000_000) + 'M'
  if (n >= 1_000) return trimZero(n / 1_000) + 'k'
  return String(n)
}

/** 0 → "$0", 0.00432 → "$0.0043", 1.5 → "$1.50". */
export function fmtCost(usd) {
  if (usd == null || isNaN(usd)) return '–'
  if (usd === 0) return '$0'
  if (usd < 0.01) return '$' + usd.toFixed(4)
  return '$' + usd.toFixed(2)
}

/** 950 → "950ms", 8000 → "8.0s", 130000 → "2m 10s". */
export function fmtDuration(ms) {
  if (ms == null || isNaN(ms) || ms < 0) return '–'
  if (ms < 1000) return Math.round(ms) + 'ms'
  if (ms < 60_000) return trimZero(ms / 1000, 1) + 's'
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return s ? `${m}m ${s}s` : `${m}m`
}

function trimZero(v, digits = 1) {
  const s = v.toFixed(digits)
  return s.endsWith('.0') ? s.slice(0, -2) : s
}

/**
 * Builds the compact parts list for a metrics strip. Only includes parts
 * with real data, so the strip never shows empty placeholders.
 */
export function metricParts(m) {
  if (!m) return []
  const parts = []
  if (m.provider || m.model) parts.push([m.provider, m.model].filter(Boolean).join('/'))
  if (m.duration_ms != null && m.duration_ms > 0) parts.push(fmtDuration(m.duration_ms))
  if (m.total_tokens > 0) {
    parts.push(`${fmtTokens(m.total_tokens)} tok (${fmtTokens(m.prompt_tokens)}↑ ${fmtTokens(m.comp_tokens)}↓)`)
  }
  if (m.cost_usd > 0) parts.push(fmtCost(m.cost_usd))
  if (m.tool_calls > 0) parts.push(`${m.tool_calls} tool${m.tool_calls === 1 ? '' : 's'}`)
  return parts
}
