// Operations reports are deterministic summaries of the displayed snapshot.
// Exports never fetch again, so their period/totals match what the user reviewed.
export const reportWindows = [
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
]
export const count = value => Number.isFinite(value) ? value.toLocaleString('en-US') : 'Unavailable'
export const spend = micros => Number.isFinite(micros) ? `$${(micros / 1000000).toFixed(4)}` : 'Unavailable'

export function validateReport(report, window) {
  if (report?.schema_version !== 1 || report.window !== window ||
      !['ready', 'partial', 'unavailable'].includes(report.status) ||
      !Number.isFinite(Date.parse(report.generated_at)) || !Number.isFinite(Date.parse(report.start)) || !Number.isFinite(Date.parse(report.end)) ||
      Date.parse(report.start) >= Date.parse(report.end) || !Array.isArray(report.notes) ||
      !report.notes.every(n => typeof n === 'string') || !report.sources) {
    throw new Error('The gateway returned an invalid operations report. Update the gateway and retry.')
  }
  const activityKeys = ['events', 'requests', 'replies', 'error_events', 'dead_letters', 'tool_calls', 'sessions', 'reply_complete_sessions', 'error_sessions', 'unresolved_sessions']
  const usageKeys = ['requests', 'attempts', 'failed', 'rejected', 'unknown_priced', 'total_tokens', 'cost_micros']
  const validCounts = (object, keys) => object && keys.every(key => Number.isSafeInteger(object[key]) && object[key] >= 0)
  for (const [key, keys] of [['activity', activityKeys], ['usage', usageKeys]]) {
    const source = report.sources[key]
    const value = report[key]
    if (!source || !['ready', 'unavailable', 'error'].includes(source.status) || typeof source.detail !== 'string' ||
        (source.status === 'ready') !== (value != null)) throw new Error('The report has inconsistent data availability.')
    if (value == null) continue
    if (!validCounts(value.totals, keys) || !Array.isArray(value.by_agent) ||
        !value.by_agent.every(row => typeof row.agent_id === 'string' && validCounts(row, keys))) {
      throw new Error('The report contains invalid counts or agent rows.')
    }
    if (key === 'usage' && (!Array.isArray(value.by_model) || !value.by_model.every(row =>
      typeof row.provider === 'string' && typeof row.model === 'string' && validCounts(row, keys)))) {
      throw new Error('The report contains invalid model rows.')
    }
    if (key === 'activity' && value.single_request_reply_latency != null &&
        !validCounts(value.single_request_reply_latency, ['samples', 'avg_ms', 'p95_ms'])) {
      throw new Error('The report contains invalid latency data.')
    }
  }
  const available = [report.activity, report.usage].filter(Boolean).length
  if (report.status !== ['unavailable', 'partial', 'ready'][available]) throw new Error('The report has inconsistent status.')
  return report
}

export function reportAttention(report) {
  const items = []
  for (const [name, source] of Object.entries(report.sources)) {
    if (source.status !== 'ready') items.push(`${name === 'activity' ? 'Activity' : 'Usage'} unavailable: ${source.detail}`)
  }
  const a = report.activity?.totals, u = report.usage?.totals
  if (a?.error_sessions) items.push(`Sessions with errors or dead letters: ${count(a.error_sessions)}. Inspect Runs to see whether they recovered.`)
  else if (a?.error_events || a?.dead_letters) items.push(`Recorded error events: ${count(a.error_events)}; dead letters: ${count(a.dead_letters)}. No matching session start was counted in this window; inspect Runs rather than treating this as error-free.`)
  if (a?.unresolved_sessions) items.push(`Sessions with fewer replies than requests: ${count(a.unresolved_sessions)}. Work may be active, interrupted, or cross the report boundary; check Runs before retrying.`)
  if (u?.failed) items.push(`Failed model requests: ${count(u.failed)}. Check provider availability and run records.`)
  if (u?.rejected) items.push(`Model requests rejected before execution: ${count(u.rejected)}. Check cost controls and admission limits.`)
  if (u?.unknown_priced) items.push(`Model requests with unknown pricing: ${count(u.unknown_priced)}. The recorded cost is not a complete bill; check provider pricing.`)
  return items
}

// Markdown is exported as a file, never inserted as HTML. Escape identifiers
// as well, so opening an export in a Markdown viewer cannot embed remote media.
const md = value => String(value ?? '').replace(/[\r\n\u0000-\u001f\u007f]/g, ' ').replace(/[\\`*_{}\[\]()<>#!|]/g, '\\$&')
export function reportMarkdown(report) {
  const a = report.activity?.totals, u = report.usage?.totals
  const lines = ['# Soulacy operations report', '', `Period (UTC): ${report.start} to ${report.end} (end exclusive)`,
    `Generated: ${report.generated_at}`, `Data status: ${report.status}`, '', '## Summary', '',
    `- Incoming requests: ${count(a?.requests)}`, `- Sessions: ${count(a?.sessions)}`,
    `- Reply-complete sessions: ${count(a?.reply_complete_sessions)}`, `- Sessions with errors: ${count(a?.error_sessions)}`,
    `- Unresolved sessions: ${count(a?.unresolved_sessions)}`, `- Tool calls: ${count(a?.tool_calls)}`,
    `- Model requests (including rejected): ${count(u?.requests)}`, `- Provider attempts: ${count(u?.attempts)}`,
    `- Failed model requests: ${count(u?.failed)}`, `- Rejected model requests: ${count(u?.rejected)}`,
    `- Tokens: ${count(u?.total_tokens)}`, `- Recorded estimated cost (USD): ${spend(u?.cost_micros)}`,
    `- Model requests with unknown pricing: ${count(u?.unknown_priced)}`, '', '## Check next', '']
  const attention = reportAttention(report)
  lines.push(...(attention.length ? attention.map(item => `- ${md(item)}`) : ['No flagged conditions in the available records. This is not a quality or delivery guarantee.']))
  if (report.activity) {
    lines.push('', '## Activity by agent', '', '| Agent | Requests | Sessions | Reply-complete | With errors | Unresolved | Tool calls |', '| --- | ---: | ---: | ---: | ---: | ---: | ---: |')
    for (const row of report.activity.by_agent) lines.push(`| ${md(row.agent_id || '(unattributed)')} | ${row.requests} | ${row.sessions} | ${row.reply_complete_sessions} | ${row.error_sessions} | ${row.unresolved_sessions} | ${row.tool_calls} |`)
    const latency = report.activity.single_request_reply_latency
    lines.push('', latency ? `Single-request reply latency: average ${latency.avg_ms} ms; p95 ${latency.p95_ms} ms; ${latency.samples} samples.` : 'Single-request reply latency: no qualifying samples.')
  }
  for (const [key, title, identify] of [['by_agent', 'Usage by agent', r => r.agent_id || '(unattributed)'], ['by_model', 'Usage by model', r => `${r.provider || '(unknown)'}/${r.model || '(unknown)'}`]]) {
    if (!report.usage) continue
    lines.push('', `## ${title}`, '', '| Name | Model requests | Attempts | Tokens | Estimated USD | Unpriced |', '| --- | ---: | ---: | ---: | ---: | ---: |')
    for (const row of report.usage[key]) lines.push(`| ${md(identify(row))} | ${row.requests} | ${row.attempts} | ${row.total_tokens} | ${spend(row.cost_micros)} | ${row.unknown_priced} |`)
  }
  lines.push('', '## Sources and limitations', '')
  for (const [key, source] of Object.entries(report.sources)) lines.push(`- ${md(key)}: ${md(source.status)} — ${md(source.detail)}`)
  lines.push(...report.notes.map(note => `- ${md(note)}`), '')
  return lines.join('\n')
}

// Quote every field and neutralize spreadsheet formula prefixes, including
// leading whitespace/control characters. CSV is a tidy metric table, with the
// same time window, availability, and limitations included in the export.
const csvCell = value => {
  let text = String(value ?? '')
  if (/^[\s\u0000-\u001f]*[=+\-@]/.test(text)) text = "'" + text
  return '"' + text.replace(/"/g, '""') + '"'
}
export function reportCSV(report) {
  const rows = [['section', 'agent', 'provider', 'model', 'metric', 'value', 'window_start_utc', 'window_end_utc']]
  const add = (section, agent, provider, model, metric, value) => rows.push([section, agent, provider, model, metric, value, report.start, report.end])
  add('metadata', '', '', '', 'status', report.status)
  add('metadata', '', '', '', 'generated_at', report.generated_at)
  for (const [name, source] of Object.entries(report.sources)) {
    add('source', '', '', '', name, `${source.status}: ${source.detail}`)
  }
  for (const note of report.notes) add('limitation', '', '', '', 'note', note)
  for (const [name, value] of [['activity', report.activity], ['usage', report.usage]]) {
    if (!value) continue
    for (const [metric, count] of Object.entries(value.totals)) add(name, '', '', '', metric, count)
    for (const row of value.by_agent) {
      for (const [metric, count] of Object.entries(row)) {
        if (!['agent_id', 'provider', 'model'].includes(metric)) add(`${name}_by_agent`, row.agent_id, '', '', metric, count)
      }
    }
  }
  for (const row of report.usage?.by_model || []) {
    for (const [metric, count] of Object.entries(row)) {
      if (!['agent_id', 'provider', 'model'].includes(metric)) add('usage_by_model', '', row.provider, row.model, metric, count)
    }
  }
  for (const [metric, value] of Object.entries(report.activity?.single_request_reply_latency || {})) add('single_request_reply_latency', '', '', '', metric, value)
  return rows.map(row => row.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

export function downloadReport(report, format) {
  const content = format === 'csv' ? reportCSV(report) : format === 'json' ? JSON.stringify(report, null, 2) : reportMarkdown(report)
  const mime = format === 'csv' ? 'text/csv' : format === 'json' ? 'application/json' : 'text/markdown'
  const blob = new Blob([content], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `soulacy-operations-${report.window}-${report.end.slice(0, 10)}.${format}`
  document.body.append(anchor)
  anchor.click(); anchor.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
