// failuregroup.js — group repeated failures, and name them (ST-13).
//
// Two gaps this closes.
//
// GROUPING (AC8). /studio/failed-runs returns a flat list, so an agent failing
// the same way on a schedule produced one row per run. Twelve identical rows do
// not tell you more than one row saying "12 times since Tuesday" — they tell you
// less, because the one genuinely different failure is now buried among them.
//
// TAXONOMY (AC5). The story asks for graph / contract / configuration /
// provider / permission / delivery / transient. The backend has two other
// vocabularies that do not match it and are not connected to each other:
// classifyFlowError returns prose, and RepairClass uses repair-oriented names
// (shape_drift, template_error, …). This maps what the server does send onto the
// vocabulary the story specifies, so the UI can group and filter by cause.
//
// The mapping lives here rather than being invented per-render, and is exported
// so it can be tested against real diagnosis payloads. It should ultimately move
// server-side — this is a display-layer stopgap, and it is honest about the
// cases it cannot name.

export const CAT_GRAPH = 'graph'
export const CAT_CONTRACT = 'contract'
export const CAT_CONFIG = 'configuration'
export const CAT_PROVIDER = 'provider'
export const CAT_PERMISSION = 'permission'
export const CAT_DELIVERY = 'delivery'
export const CAT_TRANSIENT = 'transient'
export const CAT_UNKNOWN = 'unknown'

export const CATEGORY_LABEL = {
  [CAT_GRAPH]: 'Graph',
  [CAT_CONTRACT]: 'Contract',
  [CAT_CONFIG]: 'Configuration',
  [CAT_PROVIDER]: 'Provider',
  [CAT_PERMISSION]: 'Permission',
  [CAT_DELIVERY]: 'Delivery',
  [CAT_TRANSIENT]: 'Transient',
  [CAT_UNKNOWN]: 'Unclassified',
}

// What each category means for what you should DO. A label alone still leaves
// the user deciding whether to retry or to go fix something.
export const CATEGORY_HINT = {
  [CAT_GRAPH]: 'The workflow structure is wrong — a step or wire does not resolve.',
  [CAT_CONTRACT]: 'A step produced a shape the next step could not accept.',
  [CAT_CONFIG]: 'Something is missing from setup — a credential, server or destination.',
  [CAT_PROVIDER]: 'The model or API rejected the call.',
  [CAT_PERMISSION]: 'The run was refused because it lacked consent or scope.',
  [CAT_DELIVERY]: 'The work completed but the result could not be delivered.',
  [CAT_TRANSIENT]: 'A temporary fault. Retrying unchanged may well succeed.',
  [CAT_UNKNOWN]: 'Studio could not classify this automatically — read the trace.',
}

/** Retrying unchanged is only sensible for genuinely transient faults. */
export function isRetryable(category) {
  return category === CAT_TRANSIENT
}

// The backend's repair classes, mapped onto the story's vocabulary. Kept as an
// explicit table rather than string-matching so an unmapped class is visible as
// a gap instead of silently becoming "unknown".
const REPAIR_CLASS_MAP = {
  auth: CAT_PROVIDER,
  permission: CAT_PERMISSION,
  network: CAT_TRANSIENT,
  rate_limit: CAT_TRANSIENT,
  assertion: CAT_CONTRACT,
  shape_drift: CAT_CONTRACT,
  template_error: CAT_GRAPH,
  empty_result: CAT_CONTRACT,
  tool_failure: CAT_PROVIDER,
}

/**
 * classifyFailure maps a diagnosis/failed-run record onto the story taxonomy.
 * Prefers an explicit server category, then the repair class, then a last-resort
 * read of the error text.
 */
export function classifyFailure(rec) {
  const r = rec || {}
  // 1. If the server ever starts sending a real category, it wins outright.
  const explicit = String(r.category || '').toLowerCase()
  if (CATEGORY_LABEL[explicit]) return explicit

  // 2. The repair class is structured and reliable where present.
  const cls = String(r.class || r.repair_class || '').toLowerCase()
  if (REPAIR_CLASS_MAP[cls]) return REPAIR_CLASS_MAP[cls]

  // 3. Fall back to the prose. Deliberately last, and deliberately narrow:
  //    guessing from free text is how a misleading label gets attached to a
  //    failure the user then troubleshoots in the wrong direction.
  const text = [r.root_cause, r.error, r.message].filter(Boolean).join(' ').toLowerCase()
  if (!text) return CAT_UNKNOWN
  if (/\b(consent|not permitted|forbidden|denied|unauthori[sz]ed scope)\b/.test(text)) return CAT_PERMISSION
  if (/\b(deliver|channel|telegram|slack|webhook|recipient|destination)\b/.test(text)) return CAT_DELIVERY
  if (/\b(timeout|deadline exceeded|temporarily|rate.?limit|too many requests|503|502)\b/.test(text)) return CAT_TRANSIENT
  if (/\b(api key|credential|secret|not configured|missing config|no provider)\b/.test(text)) return CAT_CONFIG
  if (/\b(unknown node|no such node|port|edge|template|render)\b/.test(text)) return CAT_GRAPH
  if (/\b(required input|missing required|not available|expected .* received|shape)\b/.test(text)) return CAT_CONTRACT
  if (/\b(provider|model|completion|4\d\d|5\d\d)\b/.test(text)) return CAT_PROVIDER
  return CAT_UNKNOWN
}

/** Collapse volatile detail so two runs of the SAME fault share a signature. */
function normalizeMessage(msg) {
  return String(msg || '')
    .toLowerCase()
    // Ids, timestamps and numbers differ per run without changing the fault.
    .replace(/\b[0-9a-f]{8,}\b/g, '<id>')
    .replace(/\b\d{4}-\d{2}-\d{2}[t ][\d:.]+z?\b/g, '<time>')
    .replace(/\b\d+(?:\.\d+)?(?:ms|s|m|h)\b/g, '<duration>')
    .replace(/\b\d+\b/g, '<n>')
    .replace(/\s+/g, ' ')
    .trim()
}

/**
 * subjectOf is what the failure is ABOUT — the failing step where a diagnosis
 * names one, otherwise the agent.
 *
 * Note that /studio/failed-runs sends `failedAt` as a TIMESTAMP, not a node.
 * Treating it as an identifier would give every run a unique signature and
 * defeat grouping entirely, which is the whole point of this module.
 */
function subjectOf(run) {
  const r = run || {}
  return r.failed_node || r.failedNode || r.node || r.agentId || r.agent_id || ''
}

/**
 * failureSignature identifies "the same failure". Subject plus category plus
 * normalised message, because the same message at two different steps is two
 * different problems.
 */
export function failureSignature(run) {
  const r = run || {}
  return `${subjectOf(r)}|${classifyFailure(r)}|${normalizeMessage(r.error || r.message || r.root_cause)}`
}

/** Best available timestamp, as epoch ms; 0 when absent. */
function timeOf(run) {
  const r = run || {}
  const t = r.failedAt || r.failed_at || r.time || r.at || r.started_at || r.created_at || ''
  const n = typeof t === 'number' ? t : Date.parse(t)
  return Number.isFinite(n) ? n : 0
}

/**
 * occurrencesOf is how many times this entry already represents. The dead-letter
 * queue retries before giving up and reports `attempts`, so counting the entry
 * as one occurrence would under-report a failure that had already been retried
 * five times.
 */
function occurrencesOf(run) {
  const n = Number((run && run.attempts) || 0)
  return Number.isFinite(n) && n > 0 ? n : 1
}

/**
 * groupFailures collapses repeated failures.
 * Returns [{ key, category, node, message, count, first, last, runs }] sorted by
 * most-recent-first, so the thing that just broke is at the top rather than the
 * thing that has broken most often.
 */
export function groupFailures(runs) {
  const list = Array.isArray(runs) ? runs.filter(Boolean) : []
  const by = new Map()
  for (const run of list) {
    const key = failureSignature(run)
    const t = timeOf(run)
    const existing = by.get(key)
    if (existing) {
      existing.count += occurrencesOf(run)
      existing.entries++
      existing.runs.push(run)
      if (t && (!existing.first || t < existing.first)) existing.first = t
      if (t > existing.last) { existing.last = t; existing.latest = run }
    } else {
      by.set(key, {
        key,
        category: classifyFailure(run),
        node: subjectOf(run),
        agentName: run.agentName || run.agent_name || '',
        message: run.error || run.message || run.root_cause || '',
        // count is total OCCURRENCES (including DLQ retries); entries is how
        // many distinct queue items back it. They differ, and conflating them
        // would either over- or under-state how bad the problem is.
        count: occurrencesOf(run),
        entries: 1,
        first: t,
        last: t,
        latest: run,
        runs: [run],
      })
    }
  }
  return [...by.values()].sort((a, b) => b.last - a.last)
}

/** Counts per category, for a filter bar that only offers non-empty options. */
export function categoryCounts(groups) {
  const out = {}
  for (const g of Array.isArray(groups) ? groups : []) {
    if (!g) continue
    out[g.category] = (out[g.category] || 0) + (g.count || 1)
  }
  return out
}
