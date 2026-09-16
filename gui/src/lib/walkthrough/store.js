// store.js — walkthrough state, held outside the page components.
//
// Pages unmount on every route change, and the tour navigates between pages by
// design, so its state cannot live in a component. It lives here, in a store,
// exactly like `studioSession` does for the Studio draft.
//
// Progress is written two places on purpose:
//   • localStorage      — instant, survives a refresh, per browser.
//   • PATCH /config     — per install, so the tour does not re-introduce itself
//                         every time you open the UI in a different browser.
// The server write only happens when the tour is paused, skipped or finished
// (three writes at most), not on every step — each PATCH rewrites config.yaml.

import { writable, get } from 'svelte/store'
import { api } from '../api.js'
import { clampIndex, walkthroughSteps, WALKTHROUGH_VERSION } from './steps.js'
import { pagesForLevel } from '../nav.js'
import { navLevel } from '../stores.js'

const LS_KEY = 'soulacy-walkthrough'

const EMPTY = {
  loaded: false,   // true once we've consulted localStorage + the gateway
  active: false,   // the overlay is on screen
  index: 0,        // current step
  seen: false,     // finished or dismissed at least once
  resumeIndex: 0,  // where the last session stopped
  version: 0,      // WALKTHROUGH_VERSION at the time `seen` was set
}

export const walkthrough = writable({ ...EMPTY })

// ── persistence ───────────────────────────────────────────────────────────────

/** Read the local mirror. Never throws — private mode / disabled storage. */
export function readLocal() {
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return null
    const v = JSON.parse(raw)
    if (!v || typeof v !== 'object') return null
    return {
      seen: v.seen === true,
      step: clampIndex(v.step),
      version: Number(v.version) || 0,
    }
  } catch (_) {
    return null
  }
}

function writeLocal(state) {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify({
      seen: state.seen === true,
      step: clampIndex(state.index),
      version: state.version || 0,
    }))
  } catch (_) { /* storage unavailable — the server copy still applies */ }
}

/** Shape of the gateway's `ui` config block, defensively parsed. */
export function fromConfig(cfg) {
  const ui = cfg && cfg.ui
  if (!ui || typeof ui !== 'object') return null
  return {
    seen: ui.walkthrough_seen === true,
    step: clampIndex(ui.walkthrough_step),
    version: Number(ui.walkthrough_version) || 0,
  }
}

/**
 * Merge the two sources. `seen` is sticky across both (dismissing it in one
 * browser should not make it pop up in another), and the furthest recorded
 * position wins so switching machines mid-tour does not lose your place.
 */
export function mergeState(local, remote) {
  const l = local || {}
  const r = remote || {}
  const version = Math.max(l.version || 0, r.version || 0)
  const seen = (l.seen === true || r.seen === true) && version >= WALKTHROUGH_VERSION
  return {
    seen,
    resumeIndex: clampIndex(Math.max(l.step || 0, r.step || 0)),
    version,
  }
}

async function pushToGateway(state) {
  try {
    await api.config.patch({
      ui: {
        walkthrough_seen: state.seen === true,
        walkthrough_step: clampIndex(state.index),
        walkthrough_version: WALKTHROUGH_VERSION,
      },
    })
  } catch (_) {
    // Older gateway, read-only config, or no write permission. The local
    // mirror already holds the same values, so the tour still behaves.
  }
}

/** Load persisted progress. Resolves once both sources have been consulted. */
export async function loadWalkthroughState() {
  const local = readLocal()
  let remote = null
  try {
    remote = fromConfig(await api.config.get())
  } catch (_) { /* not reachable / not permitted — local only */ }
  const merged = mergeState(local, remote)
  walkthrough.update((s) => ({
    ...s,
    loaded: true,
    seen: merged.seen,
    resumeIndex: merged.resumeIndex,
    version: merged.version,
    index: s.active ? s.index : merged.resumeIndex,
  }))
  return get(walkthrough)
}

// ── controls ──────────────────────────────────────────────────────────────────

/** Open the tour. `index` defaults to the start; pass `'resume'` to continue. */
export function startWalkthrough(index = 0) {
  walkthrough.update((s) => ({
    ...s,
    active: true,
    index: index === 'resume' ? clampIndex(s.resumeIndex) : clampIndex(index),
  }))
}

export function gotoStep(index) {
  walkthrough.update((s) => {
    const next = { ...s, index: clampIndex(index) }
    next.resumeIndex = next.index
    writeLocal(next)
    return next
  })
}

/**
 * Steps whose screen is not in the sidebar right now are skipped.
 *
 * The script covers every destination, which is the contract steps.test.js
 * enforces. The sidebar shows a subset depending on the chosen level, so a
 * stop for a hidden screen would dim the page and point at nothing. Intro and
 * outro have no `nav` and are always shown.
 */
function stepIsVisible(step) {
  if (!step || !step.nav) return true
  try {
    return pagesForLevel(get(navLevel)).some((p) => p.id === step.nav)
  } catch {
    return true
  }
}

function seek(from, direction) {
  let i = from
  while (i >= 0 && i < walkthroughSteps.length && !stepIsVisible(walkthroughSteps[i])) {
    i += direction
  }
  return i
}

export function nextStep() {
  const s = get(walkthrough)
  const target = seek(s.index + 1, 1)
  if (target >= walkthroughSteps.length) return finishWalkthrough()
  gotoStep(target)
}

export function prevStep() {
  const s = get(walkthrough)
  const target = seek(s.index - 1, -1)
  gotoStep(target < 0 ? 0 : target)
}

/**
 * Leave the tour partway. The position is kept so "Show me around" can offer
 * to resume rather than restarting a 24-stop tour from the top.
 */
export function pauseWalkthrough() {
  let snapshot
  walkthrough.update((s) => {
    const next = { ...s, active: false, resumeIndex: clampIndex(s.index) }
    writeLocal(next)
    snapshot = next
    return next
  })
  return pushToGateway(snapshot)
}

/** Dismiss for good: don't auto-open again, and restart from the top next time. */
export function skipWalkthrough() {
  let snapshot
  walkthrough.update((s) => {
    const next = { ...s, active: false, seen: true, index: 0, resumeIndex: 0, version: WALKTHROUGH_VERSION }
    writeLocal(next)
    snapshot = next
    return next
  })
  return pushToGateway(snapshot)
}

/** Reached the end. Same as skip, but the intent was completion. */
export function finishWalkthrough() {
  let snapshot
  walkthrough.update((s) => {
    const next = { ...s, active: false, seen: true, index: 0, resumeIndex: 0, version: WALKTHROUGH_VERSION }
    writeLocal(next)
    snapshot = next
    return next
  })
  return pushToGateway(snapshot)
}

/** True when the tour should open itself on this load. */
export function shouldAutoStart(state) {
  if (!state || state.loaded !== true || state.seen === true || state.active === true) return false
  // Not on top of the front door. Simple mode exists for someone who has not
  // used this before, and opening with a guided tour of the furniture — before
  // they have described a single thing they want — is the overwhelm the levels
  // were introduced to remove. "Show me around" is still one click away.
  try {
    if (get(navLevel) === 'simple') return false
  } catch { /* fall through to the old behaviour */ }
  return true
}

/** Reset — used by tests, and by "start over" in the resume prompt. */
export function resetWalkthrough() {
  walkthrough.set({ ...EMPTY })
}
