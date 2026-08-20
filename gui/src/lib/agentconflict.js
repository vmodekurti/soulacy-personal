// agentconflict.js — what the GUI does when a save loses a race (MU-028
// criterion 3: "the GUI offers reload, compare, and explicit overwrite/fork
// choices").
//
// The policy lives here rather than in Agents.svelte for the reason the server
// side collapsed checkIfMatch into internal/concurrency: a decision embedded in
// a component is a decision only an integration test can reach, and the
// interesting cases here are the ones that are awkward to drive through a DOM.
// The component is the shell.
//
// THE ONE RULE THAT MATTERS. "Overwrite" resends with the version the SERVER
// named in the 409 — it does not drop the precondition. So overwrite means
// "replace the thing I was just shown", not "replace whatever is there now": a
// third save landing between the conflict and the overwrite conflicts again
// rather than being silently discarded. Dropping the header would turn the
// deliberate choice into the original bug, arrived at by a different route.

import { conflictDetail, isConflict, isPreconditionRequired } from './resourceversions.js'

// CHOICES are the three offers, in the order they should be presented: the
// safe one first. A dialog whose default action discards somebody's work is a
// dialog people learn to click through.
export const CHOICES = ['reload', 'compare', 'overwrite']

/**
 * conflictFrom builds the dialog's state from a rejected save.
 *
 * Returns null for anything that is not a concurrency refusal, so a caller can
 * pass every error through it and keep its existing handling for the rest.
 *
 * `kind` distinguishes the two refusals because their remedies differ. A stale
 * save has a human on the other end and a choice to make; a missing
 * precondition is this client not participating in versioning at all, which is
 * recoverable by reloading and needs nobody's decision.
 */
export function conflictFrom(err, { agentId = '', mine = '' } = {}) {
  if (isConflict(err)) {
    return { kind: 'stale', agentId, mine, theirs: null, choice: 'reload', ...conflictDetail(err) }
  }
  if (isPreconditionRequired(err)) {
    return { kind: 'precondition', agentId, mine, theirs: null, choice: 'reload', ...conflictDetail(err) }
  }
  return null
}

/**
 * conflictHeadline is the sentence at the top of the dialog.
 *
 * It names the other person when the server could identify them. "Somebody
 * else changed this" without saying WHO is unactionable in a team, because the
 * remedy is usually a conversation the person cannot have.
 */
export function conflictHeadline(conflict) {
  if (!conflict) return ''
  if (conflict.kind === 'precondition') {
    return 'This page is out of date. Reload it before saving so your change cannot overwrite someone else’s.'
  }
  const who = (conflict.lastModifiedBy || '').trim()
  const when = formatWhen(conflict.lastModifiedAt)
  const subject = conflict.agentId ? `“${conflict.agentId}”` : 'This agent'
  if (who) {
    return `${subject} was changed by ${who}${when ? ` ${when}` : ''} since you opened it.`
  }
  return `${subject} was changed${when ? ` ${when}` : ''} since you opened it.`
}

function formatWhen(at) {
  if (!at) return ''
  const t = Date.parse(at)
  if (Number.isNaN(t)) return ''
  return `at ${new Date(t).toLocaleString()}`
}

/**
 * overwriteOptions are the request options for a deliberate replace.
 *
 * The precondition is the version the server just named, NOT an absent header.
 * See the file comment: this is the difference between "I choose to replace
 * what I was shown" and "I choose not to participate", and only the first is a
 * decision anyone made.
 */
export function overwriteOptions(conflict) {
  const current = (conflict && conflict.currentVersion) || ''
  if (!current) return null
  return { headers: { 'If-Match': current } }
}

/**
 * diffLines produces a line-level comparison of the local and server versions.
 *
 * Plain LCS. It exists so "compare" can show what would be lost rather than
 * asserting that something would be — a dialog that says "your changes
 * conflict" and shows nothing asks the user to take it on trust, and they will
 * choose Overwrite because it is the button that keeps their work.
 */
export function diffLines(mine, theirs) {
  const a = splitLines(mine)
  const b = splitLines(theirs)
  const lcs = longestCommon(a, b)
  const out = []
  let i = 0
  let j = 0
  for (const [ai, bj] of lcs) {
    while (i < ai) out.push({ kind: 'mine', text: a[i++] })
    while (j < bj) out.push({ kind: 'theirs', text: b[j++] })
    out.push({ kind: 'same', text: a[ai] })
    i = ai + 1
    j = bj + 1
  }
  while (i < a.length) out.push({ kind: 'mine', text: a[i++] })
  while (j < b.length) out.push({ kind: 'theirs', text: b[j++] })
  return out
}

/** conflictSummary counts what compare would show, for a one-line preview. */
export function conflictSummary(mine, theirs) {
  let added = 0
  let removed = 0
  for (const line of diffLines(mine, theirs)) {
    if (line.kind === 'mine') removed++
    else if (line.kind === 'theirs') added++
  }
  return { yours: removed, theirs: added, identical: removed === 0 && added === 0 }
}

function splitLines(text) {
  if (typeof text !== 'string' || text === '') return []
  return text.replace(/\r\n/g, '\n').split('\n')
}

// longestCommon returns the matched index pairs of the longest common
// subsequence. Quadratic, which is right for a SOUL.yaml: the inputs are
// hundreds of lines and the alternative is a dependency.
function longestCommon(a, b) {
  const n = a.length
  const m = b.length
  const table = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[i][j] = a[i] === b[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1])
    }
  }
  const pairs = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      pairs.push([i, j])
      i++
      j++
    } else if (table[i + 1][j] >= table[i][j + 1]) i++
    else j++
  }
  return pairs
}
