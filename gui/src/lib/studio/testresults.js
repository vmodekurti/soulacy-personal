// testresults.js — map a workflow test-run result onto individual steps so the
// Plan view can show pass/fail next to each block (Guided Studio Builder, Story
// 12 "test results shown next to each relevant step"). Pure & unit-tested.

// asObject safely coerces a possibly-JSON-string output into an object.
function asObject(v) {
  if (v && typeof v === 'object') return v
  if (typeof v === 'string') {
    try { return JSON.parse(v) } catch { return null }
  }
  return null
}

// stepError returns a non-empty error string when a trace step failed, mirroring
// Studio's stepToolError logic.
export function stepError(step) {
  if (step && step.error) return String(step.error)
  const o = asObject(step && step.output)
  if (o && (o.status === 'error' || o.status === 'failed')) return o.error || o.message || 'tool returned an error'
  if (o && o.error) return String(o.error)
  return ''
}

// stepResultsByNode returns { [nodeId]: { ok, error, durationMs, mocked } } for
// every step in a test result's trace.
export function stepResultsByNode(testResult) {
  const map = {}
  const trace = (testResult && testResult.trace) || []
  for (const s of trace) {
    if (!s || !s.nodeId) continue
    const err = stepError(s)
    map[s.nodeId] = { ok: !err, error: err, durationMs: s.durationMs ?? null, mocked: !!s.mocked }
  }
  return map
}

// firstFailedNode returns the id of the first failing step, or '' if all passed.
export function firstFailedNode(testResult) {
  const trace = (testResult && testResult.trace) || []
  for (const s of trace) {
    if (s && s.nodeId && stepError(s)) return s.nodeId
  }
  return ''
}
