// pyinfer.js — frontend mirror of internal/studio/pyinfer.go. Detects when a
// step description implies deterministic computation and should become a Python
// step, with the plain-English reason (Guided Studio Builder, Stories 3 & 4).
// Kept in sync with the Go version so the UI can suggest Python steps without a
// round-trip. Pure & unit-tested.

const TRIGGERS = [
  { kws: ['clean', 'dedup', 'deduplicate', 'normalize', 'tidy', 'sanitize', 'sanitise'],
    template: 'clean_csv', label: 'Clean Spreadsheet',
    reason: 'Python is used here because cleaning tabular data needs deterministic, repeatable rules.' },
  { kws: ['rank', 'ranking', 'sort', 'score', 'prioritize', 'prioritise'],
    template: 'calculate_metrics', label: 'Rank Items',
    reason: 'Python is used here because ranking requires deterministic, reproducible calculations.' },
  { kws: ['calculate', 'compute', 'sum', 'average', 'metric', 'ratio', 'aggregate', 'total'],
    template: 'calculate_metrics', label: 'Calculate Metrics',
    reason: 'Python is used here because numeric calculations must be exact and reproducible.' },
  { kws: ['chart', 'graph', 'plot', 'series', 'visuali'],
    template: 'chart_data', label: 'Prepare Chart Data',
    reason: 'Python is used here because preparing chart series requires deterministic aggregation.' },
  { kws: ['validate', 'verify records', 'check records', 'required field'],
    template: 'validate_records', label: 'Validate Records',
    reason: 'Python is used here because validation rules must be applied deterministically to every record.' },
  { kws: ['transform', 'reshape', 'convert', 'parse', 'restructure', 'map fields', 'extract fields'],
    template: 'transform_json', label: 'Transform Data',
    reason: 'Python is used here because reshaping or parsing structured data is a deterministic transformation.' },
]

function tokenize(t) {
  return t.split(/[^a-z]+/).filter(Boolean)
}

function matchKeyword(text, tokens, kw) {
  if (kw.includes(' ')) return text.includes(kw)
  for (const tok of tokens) {
    if (tok === kw) return true
    if (kw.length >= 4 && (tok === kw + 's' || tok === kw + 'd' || tok === kw + 'ed' || tok === kw + 'ing')) return true
  }
  return false
}

// inferPython inspects a task/step description and returns
// { needsPython, reason, template, label }. needsPython is false when nothing
// deterministic is implied.
export function inferPython(text) {
  const t = String(text || '').toLowerCase()
  const tokens = tokenize(t)
  for (const tr of TRIGGERS) {
    for (const kw of tr.kws) {
      if (matchKeyword(t, tokens, kw)) {
        return { needsPython: true, reason: tr.reason, template: tr.template, label: tr.label }
      }
    }
  }
  return { needsPython: false, reason: '', template: '', label: '' }
}

// suggestPythonSteps scans a workflow's work nodes and returns suggestions for
// steps whose description implies computation but that aren't Python steps yet.
// Each: { nodeId, label, template, reason }.
export function suggestPythonSteps(workflow) {
  const nodes = workflow?.flow?.nodes || []
  const out = []
  for (const n of nodes) {
    if (!n || n.kind === 'python' || n.kind === 'trigger' || n.kind === 'exit') continue
    const text = `${n.description || ''} ${n.tool || ''} ${n.agent || ''}`
    const inf = inferPython(text)
    if (inf.needsPython) out.push({ nodeId: n.id, label: inf.label, template: inf.template, reason: inf.reason })
  }
  return out
}
