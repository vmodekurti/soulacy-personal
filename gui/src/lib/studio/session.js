// session.js — what Studio carries across a navigation, and what it must not.
//
// Studio is mounted and destroyed by the router, so anything in component state
// is lost when the user visits another screen. A module-level snapshot preserves
// work-in-progress. Getting the RULES of that snapshot wrong produced two bugs
// that looked unrelated and shared one cause:
//
//   • Clicking Studio in the nav reopened whatever was last on the canvas —
//     including a draft that had just been SAVED — so there was no way back to
//     the home screen except reloading the page.
//   • Deleting an agent left it on screen, because clearing the live canvas did
//     not clear the snapshot, and because the snapshot dropped the agent's id,
//     so the delete could not tell that the canvas was showing that agent.
//
// These functions are pure so the rules can be tested; the component owns the
// store and the DOM.

/**
 * snapshotSession decides what to persist when Studio unmounts.
 *
 * Returns null to mean "carry nothing". Two cases:
 *   - committed: the draft was saved. It is a real agent now, listed under
 *     "Continue existing work"; keeping a private in-progress copy would reopen
 *     it as unfinished every time Studio is opened.
 *   - no workflow: there is nothing to carry, and snapshotting would resurrect
 *     something the user just cleared or deleted.
 */
export function snapshotSession(state) {
  const s = state || {}
  if (s.committed || !s.workflow) return null
  return {
    intent: s.intent || '',
    rawPrompt: s.rawPrompt || '',
    workflow: s.workflow,
    // Identity travels with the draft. Without it a restored canvas cannot be
    // matched back to the agent it came from, so deleting that agent leaves it
    // on screen and the next save creates a duplicate instead of updating.
    loadedAgentId: s.loadedAgentId || '',
    notes: s.notes || [],
    questions: s.questions || [],
    suggestions: s.suggestions || [],
    explanation: s.explanation || null,
    refinement: s.refinement || null,
    refineAnswers: s.refineAnswers || {},
  }
}

/**
 * restoreLandingStep is the step Studio opens on after restoring a session.
 *
 * Always the first step. Opening Studio from the nav is a deliberate "start
 * something / pick something" action; computing a "resume where you left off"
 * step meant the user was dropped back into the middle of an agent and could not
 * reach the home screen at all. The draft is still restored and one click away
 * on the step rail, so landing at the start costs nothing.
 */
export function restoreLandingStep(firstStepId) {
  return firstStepId
}

/**
 * sessionAfterDelete returns the session that should remain once an agent is
 * deleted. Anything showing that agent is dropped, whether it is the live canvas
 * or a snapshot taken before the user navigated away.
 */
export function sessionAfterDelete(session, deletedAgentId) {
  if (!session || !deletedAgentId) return session || null
  return session.loadedAgentId === deletedAgentId ? null : session
}

/**
 * promptsForDraft returns the prompt boxes that belong to a draft being OPENED.
 *
 * The rule that matters is the empty one: a draft with no stored prompt (a
 * template, an import, a hand-authored SOUL.yaml) must clear the boxes, not
 * inherit the previous draft's text. Leaving the old text there showed a prompt
 * describing an agent that was no longer on the canvas — and Generate would have
 * rebuilt from it, silently replacing the draft the user had just opened.
 */
export function promptsForDraft(wf) {
  const w = wf || {}
  return {
    intent: typeof w.intent === 'string' ? w.intent : '',
    rawPrompt: typeof w.raw_intent === 'string' ? w.raw_intent : '',
  }
}
