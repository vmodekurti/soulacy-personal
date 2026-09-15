// personmodel.js — presentation helpers for the person model.
//
// Kept out of the component so the rules that decide what a person reads
// about themselves can be tested directly. Two of them matter: a line must
// say where it came from, and a guess must never look like a fact.

export const SECTION_TITLES = {
  identity: 'Who you are',
  routine: 'Your usual day',
  state: 'Right now',
  relationships: 'People who matter',
  commitments: 'Open commitments',
  preferences: 'Preferences',
}

export const SECTION_ORDER = ['identity', 'routine', 'state', 'relationships', 'commitments', 'preferences']

/** What each section is for, in the second person. */
export const SECTION_BLURBS = {
  identity: 'Your name, where you live and work.',
  routine: 'The shape of a normal week, and how far today departs from it.',
  state: 'How you are at this moment. These lines expire within the hour.',
  relationships: 'The people you deal with most, and what is outstanding with each.',
  commitments: 'What you owe and are owed.',
  preferences: 'How you want to be helped.',
}

/**
 * provenance describes where one entry came from, in words a person can act on.
 * Returns { label, tone, detail } where tone is 'you' | 'agent' | 'sense'.
 */
export function provenance(entry) {
  const source = (entry?.source || '').trim()
  const said = entry?.value?.said
  if (source === 'manual') {
    return { label: 'You told us', tone: 'you', detail: '' }
  }
  if (said) {
    // An agent that can quote you is not guessing; the gateway only lets it
    // attach words you actually said.
    return { label: 'You told us', tone: 'you', detail: `“${said}”` }
  }
  if (source.startsWith('agent:')) {
    return { label: `Guessed by ${source.slice(6)}`, tone: 'agent', detail: '' }
  }
  if (source.startsWith('sense:')) {
    return { label: `Noticed by ${source.slice(6)}`, tone: 'sense', detail: '' }
  }
  return { label: source || 'Unknown', tone: 'sense', detail: '' }
}

/** True when an entry is a guess the person should read as one. */
export function isGuess(entry) {
  if (!entry) return false
  if (entry.source === 'manual' || entry.value?.said) return false
  return (entry.confidence ?? 1) < 0.7
}

/** Sections in display order, skipping ones with nothing in them. */
export function filledSections(sections = {}) {
  return SECTION_ORDER
    .filter((id) => (sections[id] || []).length > 0)
    .map((id) => ({ id, title: SECTION_TITLES[id], blurb: SECTION_BLURBS[id], entries: sections[id] }))
}

/**
 * fillProgress is a rough sense of how much Soulacy knows, so the first week
 * feels like progress rather than silence. Deliberately generous at the
 * start: three lines is real progress from nothing.
 */
export function fillProgress(sections = {}) {
  const filled = SECTION_ORDER.filter((id) => (sections[id] || []).length > 0).length
  const total = SECTION_ORDER.length
  const entries = SECTION_ORDER.reduce((n, id) => n + (sections[id] || []).length, 0)
  return { filled, total, entries, percent: Math.round((filled / total) * 100) }
}

/** A short line telling the person what to do next, or nothing when full. */
export function nextStep(sections = {}, senses = []) {
  const empty = SECTION_ORDER.filter((id) => (sections[id] || []).length === 0)
  if (!empty.length) return ''
  if (empty.length === SECTION_ORDER.length) {
    return 'Nothing yet. Chat with the Getting to Know You agent, or add a line yourself.'
  }
  const anySense = senses.some((s) => s.enabled)
  if (!anySense && (empty.includes('routine') || empty.includes('state'))) {
    return 'Switch on a sense below to let Soulacy fill in your routine on its own.'
  }
  return `Still blank: ${empty.map((id) => SECTION_TITLES[id].toLowerCase()).join(', ')}.`
}
