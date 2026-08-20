// destructive.js — MU-029 criterion 4: "destructive dialogs display workspace
// identity."
//
// THE FAILURE. Somebody belongs to Acme and to Contoso, has both open, and
// deletes "support-bot". The dialog says `Delete agent "support-bot"? This
// cannot be undone.` — a sentence that is exactly as true in the workspace
// they meant as in the one they are actually in. Agent IDs are unique per
// workspace, not per deployment, so the same name existing in both is the
// ordinary case rather than a coincidence, and the confirmation step that
// exists to prevent the mistake cannot distinguish it.
//
// TWO WRAPPERS, NOT ONE, AND NO RAW `confirm`. The distinction the guard in
// destructive.guard.test.js enforces is not "is this dialog scary" — it is
// whether anything outside this browser tab changes:
//
//   confirmDestructive  changes server state that belongs to a workspace, so
//                       the workspace has to be named.
//   confirmLocal        discards something this tab is holding — unsaved
//                       edits, a canvas regeneration. Naming a workspace there
//                       is noise, and noise is what teaches people to stop
//                       reading dialogs.
//
// Making "raw confirm()" unavailable is the point. A new destructive dialog
// then cannot be added without choosing, and choosing wrongly is a claim in
// the diff rather than an omission nobody can see.

import { get } from 'svelte/store'
import { activeWorkspace, workspaceLabel } from './workspace.js'

/**
 * confirmDestructive asks for confirmation of an action that changes state
 * belonging to a workspace.
 *
 * The workspace goes on its own line rather than being spliced into the
 * sentence: a reader scanning a modal reads the first line and the buttons,
 * and a qualifier buried mid-sentence is the part they skip. In a personal
 * deployment there is nothing to disambiguate, so the message is byte-
 * identical to what it has always been (product invariant 7).
 */
export function confirmDestructive(message) {
  const text = destructivePrompt(message, get(activeWorkspace))
  try {
    return window.confirm(text)
  } catch (_) {
    // jsdom and headless contexts throw rather than prompting. Treating that
    // as confirmation matches the existing call sites, which each wrapped
    // confirm in try/catch and defaulted to true.
    return true
  }
}

/**
 * destructivePrompt is the pure half, so the wording is testable without a
 * DOM — the same split the MU-028 conflict dialog uses, for the same reason.
 */
export function destructivePrompt(message, ws) {
  const label = workspaceLabel(ws)
  // Only in a multi-user deployment. A single-workspace install has nothing to
  // disambiguate, and a line naming the only workspace there is is exactly the
  // "noticing" invariant 7 forbids.
  if (!label || !ws?.deploymentMode || ws.deploymentMode === 'personal') return message
  return `${message}\n\nWorkspace: ${label}`
}

/**
 * confirmLocal asks for confirmation of something that only affects this tab.
 *
 * It exists so that "this one does not need a workspace" is written down at
 * the call site instead of being indistinguishable from an oversight.
 */
export function confirmLocal(message) {
  try {
    return window.confirm(message)
  } catch (_) {
    return true
  }
}
