// wizard.js — the Describe → Build → Test → Save step model.
//
// Studio was a canvas with tabs, which meant the order of operations was
// implicit: nothing told you that describing came before building, or that you
// had never tested the thing you were about to deploy. The wizard makes that
// sequence explicit.
//
// The hard part is gating, and the failure mode to avoid is a wizard that traps
// people. Two rules:
//
//  1. A step is REACHABLE as soon as it can show something useful. Build is
//     reachable with a draft even if the draft is broken — that is precisely
//     when you need to look at it. Gating on validity would hide the canvas
//     exactly when the user needs to fix the graph.
//
//  2. Only ACTIONS are gated, never navigation. You can always open Save to see
//     why you cannot save. A step that refuses to open cannot explain itself,
//     which is how wizards end up feeling like they are arguing with you.
//
// `done` therefore means "this step's work has been accomplished", not "the user
// clicked through it" — a step can be revisited without losing that status.

export const STEP_DESCRIBE = 'describe'
export const STEP_BUILD = 'build'
export const STEP_TEST = 'test'
export const STEP_SAVE = 'save'

export const STEPS = [
  { id: STEP_DESCRIBE, label: 'Describe' },
  { id: STEP_BUILD, label: 'Build' },
  { id: STEP_TEST, label: 'Test' },
  { id: STEP_SAVE, label: 'Save' },
]

/** Index of a step id, or -1. */
export function stepIndex(id) {
  return STEPS.findIndex((s) => s.id === id)
}

/**
 * ctx describes what the user has actually accomplished:
 *   { intent, hasNodes, isAgent, tested, testPassed, readiness, saved }
 * All optional — a missing field reads as "not done yet" rather than throwing.
 */
function has(v) {
  return typeof v === 'string' ? !!v.trim() : !!v
}

/**
 * intentOf is the prompt Studio should actually build from.
 *
 * The Describe step has two boxes: "Your prompt" and an optional "Refined
 * prompt". Refining is a convenience, not a prerequisite, so an empty refined
 * box must fall through to what the user typed.
 *
 * This exists because generate() read the refined box alone and returned
 * silently when it was empty — so typing in the main box and pressing Generate
 * did nothing whatsoever: no request, no error, no explanation. The spec panel
 * used this fallback and therefore filled in and enabled its Generate button,
 * which is exactly what made a dead button look like a working one. Exported so
 * the panel, the two Generate buttons and the compile call cannot drift apart
 * about which text is being built.
 */
export function intentOf(refined, raw) {
  return String(refined || '').trim() || String(raw || '').trim()
}

/**
 * generatedMode enforces Studio's safe authoring default.
 *
 * A refiner/model may still recommend "workflow", but generated fixed graphs
 * are experimental and require the dedicated Workflow opt-in. Ordinary
 * Generate therefore treats a stale/model-produced Workflow recommendation as
 * Auto; the server-side advisor can still select Plan-Execute from the intent.
 * Malformed or missing recommendations also fall back to Auto.
 */
export function generatedMode(mode) {
  const m = String(mode || '').trim().toLowerCase()
  if (m === 'workflow') return 'auto'
  if (m === 'auto' || m === 'react' || m === 'plan_execute') return m
  return 'auto'
}

/** True when the draft can be opened in Build — a broken draft still counts. */
export function hasDraft(ctx) {
  const c = ctx || {}
  return !!(c.hasNodes || c.isAgent)
}

/**
 * canEnter reports whether a step can be OPENED, plus why not.
 * Navigation is deliberately permissive; see the note at the top.
 */
export function canEnter(id, ctx) {
  const c = ctx || {}
  switch (id) {
    case STEP_DESCRIBE:
      return { ok: true, reason: '' }
    case STEP_BUILD:
      // Reachable with either a draft to look at or an intent to generate from.
      if (hasDraft(c) || has(c.intent)) return { ok: true, reason: '' }
      return { ok: false, reason: 'Describe what you want first — there is nothing to build yet.' }
    case STEP_TEST:
      if (hasDraft(c)) return { ok: true, reason: '' }
      return { ok: false, reason: 'Generate a workflow before testing it.' }
    case STEP_SAVE:
      // Always openable once something exists, INCLUDING when it cannot be
      // saved: the step is where the reasons live.
      if (hasDraft(c)) return { ok: true, reason: '' }
      return { ok: false, reason: 'There is nothing to save yet.' }
    default:
      return { ok: false, reason: 'Unknown step.' }
  }
}

/** Whether a step's work is accomplished (not merely visited). */
export function isDone(id, ctx) {
  const c = ctx || {}
  switch (id) {
    case STEP_DESCRIBE:
      return has(c.intent)
    case STEP_BUILD:
      return hasDraft(c)
    case STEP_TEST:
      // Having run a test is not the same as it passing. Only a passing run
      // marks this done — otherwise the rail would suggest a red test was
      // progress.
      return !!c.testPassed
    case STEP_SAVE:
      return !!c.saved
    default:
      return false
  }
}

/**
 * stepStates returns the rail model:
 *   [{ id, label, index, status, reason, enterable }]
 * status: 'done' | 'active' | 'blocked' | 'todo'
 */
export function stepStates(activeId, ctx) {
  return STEPS.map((s, i) => {
    const enter = canEnter(s.id, ctx)
    const done = isDone(s.id, ctx)
    let status
    if (s.id === activeId) status = 'active'
    else if (done) status = 'done'
    else if (!enter.ok) status = 'blocked'
    else status = 'todo'
    return {
      id: s.id, label: s.label, index: i + 1,
      status, reason: enter.reason, enterable: enter.ok, done,
    }
  })
}

/**
 * nextStep / prevStep give the rail's forward/back buttons a target, skipping
 * any step that cannot be entered so the button never lands on a dead end.
 */
export function nextStep(activeId, ctx) {
  for (let i = stepIndex(activeId) + 1; i < STEPS.length; i++) {
    if (canEnter(STEPS[i].id, ctx).ok) return STEPS[i].id
  }
  return ''
}
export function prevStep(activeId) {
  const i = stepIndex(activeId)
  return i > 0 ? STEPS[i - 1].id : ''
}

/**
 * autoStep picks the step to land on after a state change, used when Studio
 * loads a draft or finishes generating. It returns the FIRST step whose work is
 * not done, so a freshly generated workflow lands on Test rather than dumping
 * the user back at Describe.
 *
 * Never moves backwards past the step the user is on: yanking someone from Save
 * to Test because an edit invalidated a test result would steal their place.
 */
export function autoStep(activeId, ctx) {
  const firstUndone = STEPS.find((s) => !isDone(s.id, ctx) && canEnter(s.id, ctx).ok)
  const target = firstUndone ? firstUndone.id : STEP_SAVE
  const cur = stepIndex(activeId)
  return stepIndex(target) < cur ? activeId : target
}

/**
 * saveBlockedReason explains why the Save ACTION is unavailable, separately
 * from whether the Save step can be opened. Returns '' when saving is allowed.
 */
export function saveBlockedReason(ctx) {
  const c = ctx || {}
  if (!hasDraft(c)) return 'There is nothing to save yet.'
  const r = c.readiness
  if (!r) return ''
  if (Array.isArray(r.blockers) && r.blockers.length) {
    return `${r.blockers.length} blocker${r.blockers.length === 1 ? '' : 's'} must be resolved first.`
  }
  // A check that did not run is not a check that passed.
  if (Array.isArray(r.unknown) && r.unknown.length) {
    return `${r.unknown.length} readiness check${r.unknown.length === 1 ? '' : 's'} could not run, so this cannot be confirmed safe to save.`
  }
  return ''
}
