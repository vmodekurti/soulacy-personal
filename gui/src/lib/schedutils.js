// Schedule page helpers (Story 12). Pure functions, tested in
// schedutils.test.js.

/**
 * Label for the "Missed runs" column: how an agent behaves when the
 * gateway was down at its scheduled time.
 *   catch_up: true  → "catch up · 24h window"
 *   otherwise       → "skip"
 */
export function catchupLabel(entry) {
  if (!entry || !entry.catch_up) return 'skip'
  const w = entry.catch_up_window || '24h'
  return `catch up · ${w} window`
}

/** Tooltip explaining the missed-run behaviour in full sentences. */
export function catchupTitle(entry) {
  if (!entry || !entry.catch_up) {
    return 'Fires missed while the gateway was down are skipped. ' +
      'Enable run_missed_on_startup in the agent schedule to catch up the latest missed fire on restart.'
  }
  const w = entry.catch_up_window || '24h'
  return `If the gateway was down at the scheduled time, the LATEST missed fire within ${w} runs once at startup. Older missed fires are never replayed, and completed fires are remembered across restarts.`
}
