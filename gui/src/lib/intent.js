// intent.js — deciding what a request actually is.
//
// Soulacy had two front doors: one that answered questions and one that built
// agents, and the person had to pick before they had typed anything. Nobody
// arrives thinking "I need an agent". They think "I want a market summary at
// five", and whether that becomes a saved, scheduled agent or a single answer
// is an implementation detail they should never have to decide.
//
// So one input, and this decides. The test is recurrence, not vocabulary: does
// this need to happen again, on a schedule, or without the person present? If
// yes it is worth building something. If not, answering is faster and leaves
// nothing behind to maintain.
//
// It is deliberately deterministic. Asking a model to route would add ten to
// twenty seconds before anything happens, on the one screen where waiting is
// most expensive.

/** Words that mean "again", not "now". */
const RECURRENCE = [
  'every', 'each', 'daily', 'nightly', 'weekly', 'monthly', 'hourly',
  'recurring', 'repeatedly', 'regularly', 'on a schedule', 'from now on',
  'whenever', 'any time', 'anytime', 'always',
]

/** Standing instructions: act without me, later, when something happens. */
const STANDING = [
  'remind me', 'alert me', 'notify me', 'let me know when', 'tell me when',
  'watch ', 'monitor ', 'keep an eye', 'track ', 'check for', 'ping me',
]

/** A clock time is a strong signal that something runs unattended. */
const TIME = /\b(\d{1,2})(:\d{2})?\s*(am|pm)\b|\b\d{1,2}:\d{2}\b|\bat\s+\d{1,2}\b/i

/** A named day usually means "that day, every week". */
const WEEKDAY = /\b(monday|tuesday|wednesday|thursday|friday|saturday|sunday|weekday|weekend)s?\b/i

/**
 * Does this describe something that should keep happening?
 *
 * Returns the matched reason as well as the verdict, because a routing
 * decision the user cannot see is one they cannot correct — the screen says
 * what it concluded and offers the other path.
 */
export function classifyRequest(text) {
  const t = String(text || '').toLowerCase().trim()
  if (!t) return { kind: 'ask', reason: '' }

  for (const word of RECURRENCE) {
    if (t.includes(word)) return { kind: 'build', reason: word.trim() }
  }
  for (const phrase of STANDING) {
    if (t.includes(phrase)) return { kind: 'build', reason: phrase.trim() }
  }
  // A time or a weekday only counts alongside something to do — "what time is
  // it" and "is the market open on Friday" are questions, not standing orders.
  if ((TIME.test(t) || WEEKDAY.test(t)) && !isQuestion(t)) {
    return { kind: 'build', reason: 'a specific time' }
  }
  return { kind: 'ask', reason: '' }
}

/** A plain question: answer it, do not build anything. */
export function isQuestion(text) {
  const t = String(text || '').toLowerCase().trim()
  if (t.endsWith('?')) return true
  return /^(what|who|when|where|why|how|which|is|are|was|were|do|does|did|can|could|should|would|tell me about|explain|summarise|summarize)\b/.test(t)
}
