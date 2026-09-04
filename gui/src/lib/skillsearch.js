// skillsearch — shared search/ranking for installed skills.
//
// Used by two callers that must agree on what "searching skills" means:
//   - the Skills page filter box (filters the installed list)
//   - the Chat "/" picker (inline autocomplete in the composer)
//
// Ranking matters more than filtering here. A plain substring filter over
// name+description buries an exact name match underneath every skill whose
// description happens to mention the word — so typing "pdf" would not put the
// "pdf" skill first. Matches are therefore tiered:
//
//   0  exact name
//   1  name starts with the query
//   2  name contains the query
//   3  description contains the query
//
// Ties inside a tier are broken alphabetically, so the order is stable and
// keyboard selection never jumps around between keystrokes.

const EXACT = 0
const PREFIX = 1
const NAME = 2
const DESC = 3

/** Rank of a single skill against a lowercased query, or -1 for no match. */
function rankOf(skill, q) {
  const name = String(skill?.name || '').toLowerCase()
  const desc = String(skill?.description || '').toLowerCase()

  if (name === q) return EXACT
  if (name.startsWith(q)) return PREFIX
  if (name.includes(q)) return NAME
  if (desc.includes(q)) return DESC
  return -1
}

/**
 * Search installed skills by name and description.
 *
 * An empty query returns every skill, alphabetically — the picker and the
 * filter both open with a full list rather than nothing.
 *
 * @param {Array<{name: string, description?: string}>} skills
 * @param {string} query
 * @param {{limit?: number}} [opts]
 */
export function searchSkills(skills, query, opts = {}) {
  const list = Array.isArray(skills) ? skills : []
  const q = String(query || '').trim().toLowerCase()
  const limit = opts.limit

  let out
  if (q === '') {
    out = [...list].sort(byName)
  } else {
    out = list
      .map(s => ({ s, r: rankOf(s, q) }))
      .filter(x => x.r >= 0)
      .sort((a, b) => (a.r - b.r) || byName(a.s, b.s))
      .map(x => x.s)
  }

  return limit > 0 ? out.slice(0, limit) : out
}

function byName(a, b) {
  return String(a?.name || '').localeCompare(String(b?.name || ''))
}

/**
 * Parse a leading "/query" from the composer text.
 *
 * Returns the query when the caret is inside a slash token at the START of the
 * message, else null. Deliberately narrow: a "/" mid-sentence (a URL, a path,
 * a date) must not pop the picker open while someone is typing prose.
 *
 * @param {string} text  full composer contents
 * @param {number} caret caret index within text
 */
export function parseSlashQuery(text, caret) {
  const s = String(text || '')
  const pos = typeof caret === 'number' ? caret : s.length
  if (!s.startsWith('/')) return null

  // Only while the caret is still within the first token — once the user types
  // a space, they are writing the message, not choosing a skill.
  const head = s.slice(0, pos)
  if (/\s/.test(head)) return null

  return head.slice(1)
}

/** Replace the leading "/query" token with the chosen skill name. */
export function applySkillChoice(text, skillName) {
  const s = String(text || '')
  const rest = s.replace(/^\/\S*/, '')
  return `/${skillName}${rest.startsWith(' ') ? '' : ' '}${rest}`
}
