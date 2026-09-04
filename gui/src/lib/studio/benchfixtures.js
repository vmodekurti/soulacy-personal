// benchfixtures.js — move the test bench's state onto the workflow.
//
// Mocks, assertions, the sample input, variables and the environment used to be
// component-local state in Studio.svelte. That meant the fixtures someone built
// up to reproduce a bug disappeared the moment they navigated away, and a
// reviewer opening the workflow had no way to re-run what its author tested.
// The draft already had the right home for this (`workflow.outcome`) and simply
// was never written to.
//
// Two shapes have to be reconciled:
//   • On the wire, mocks are `{ [nodeId]: <raw JSON value> }`.
//   • In the editor they are `{ [nodeId]: "<the text the user typed>" }`,
//     because half-typed JSON must survive a keystroke without being destroyed.
// So hydration stringifies and persistence parses, and anything that does not
// parse is dropped rather than written back as a string — a mock that is not
// JSON is not a mock, and silently persisting the broken text would make the
// next load fail in a place far from the typo.

/** Stable pretty-print so hydrated mock text does not churn the editor. */
function toText(value) {
  if (value === undefined || value === null) return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch (_) {
    return ''
  }
}

/**
 * fixturesFromWorkflow reads the saved bench state off a draft.
 * Always returns a fully-populated object so callers can assign it directly.
 */
export function fixturesFromWorkflow(workflow) {
  const o = (workflow && workflow.outcome) || {}
  const mocks = o.mocks && typeof o.mocks === 'object' ? o.mocks : {}
  const mockText = {}
  for (const [nodeId, value] of Object.entries(mocks)) {
    const text = toText(value)
    if (text) mockText[nodeId] = text
  }
  return {
    assertions: Array.isArray(o.assertions)
      ? o.assertions.map((a) => ({
          target: (a && a.target) || 'result',
          op: (a && a.op) || 'contains',
          value: (a && a.value) || '',
        }))
      : [],
    mockText,
    sampleInput: o.sample_input || '',
    variables: o.variables && typeof o.variables === 'object' ? { ...o.variables } : {},
    environment: o.environment && typeof o.environment === 'object' ? { ...o.environment } : {},
    startNode: o.start_node || '',
  }
}

/**
 * outcomeWithFixtures returns the outcome block to store on the draft, merging
 * the bench state over whatever goal/enforce the draft already carries.
 *
 * Returns undefined when there is nothing worth persisting, so a workflow that
 * was never tested keeps a clean `outcome`-free shape instead of gaining an
 * empty block that shows up as noise in every diff.
 */
export function outcomeWithFixtures(existing, bench) {
  const prev = existing || {}
  const b = bench || {}

  const assertions = (b.assertions || [])
    .filter((a) => a && a.target && a.op)
    .map((a) => ({
      target: a.target,
      op: a.op,
      // `exists` takes no operand; storing one would imply a comparison the
      // runner never makes.
      value: a.op === 'exists' ? '' : a.value || '',
    }))

  const mocks = {}
  for (const [nodeId, text] of Object.entries(b.mockText || {})) {
    const trimmed = (text || '').trim()
    if (!trimmed) continue
    try {
      mocks[nodeId] = JSON.parse(trimmed)
    } catch (_) {
      // Invalid JSON is skipped — see the note at the top of this file.
    }
  }

  const variables = pruneEmpty(b.variables)
  const environment = pruneEmpty(b.environment)
  const sampleInput = (b.sampleInput || '').trim()
  const startNode = (b.startNode || '').trim()

  const out = {}
  if (prev.goal) out.goal = prev.goal
  if (prev.enforce) out.enforce = prev.enforce
  if (assertions.length) out.assertions = assertions
  if (Object.keys(mocks).length) out.mocks = mocks
  if (sampleInput) out.sample_input = sampleInput
  if (Object.keys(variables).length) out.variables = variables
  if (Object.keys(environment).length) out.environment = environment
  if (startNode) out.start_node = startNode

  return Object.keys(out).length ? out : undefined
}

/** Drop blank keys/values so an emptied row does not persist as noise. */
function pruneEmpty(map) {
  const out = {}
  for (const [k, v] of Object.entries(map || {})) {
    const key = (k || '').trim()
    if (!key) continue
    out[key] = v == null ? '' : String(v)
  }
  return out
}

/**
 * hasFixtures reports whether a draft carries any saved bench state, so the UI
 * can show "this workflow has a saved test suite" without re-deriving it.
 */
export function hasFixtures(workflow) {
  const o = (workflow && workflow.outcome) || {}
  return !!(
    (Array.isArray(o.assertions) && o.assertions.length) ||
    (o.mocks && Object.keys(o.mocks).length) ||
    o.sample_input ||
    (o.variables && Object.keys(o.variables).length) ||
    (o.environment && Object.keys(o.environment).length) ||
    o.start_node
  )
}
