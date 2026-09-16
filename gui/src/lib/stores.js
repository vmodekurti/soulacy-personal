import { writable } from 'svelte/store'

// Authentication credentials live only for the current browser tab. Migrating
// away from localStorage limits exposure after logout, browser restarts, or a
// later origin-level script compromise.
function sessionWritable(key, initial) {
  const legacy = localStorage.getItem(key)
  if (legacy !== null) {
    sessionStorage.setItem(key, legacy)
    localStorage.removeItem(key)
  }
  const stored = sessionStorage.getItem(key)
  const store = writable(stored !== null ? stored : initial)
  store.subscribe(val => {
    if (val) sessionStorage.setItem(key, val)
    else sessionStorage.removeItem(key)
  })
  return store
}

/**
 * How much of the sidebar to show. Persisted in localStorage rather than the
 * session, because it is a preference about this person, not a credential —
 * having to re-choose it on every visit would defeat the point.
 *
 * Defaults to simple. A returning power user flips it once; a first-time user
 * never has to look at 26 destinations to find the one that matters.
 */
function localWritable(key, initial) {
  let start = initial
  try {
    const v = localStorage.getItem(key)
    if (v) start = v
  } catch { /* private window, blocked storage */ }
  const store = writable(start)
  store.subscribe((v) => {
    try { localStorage.setItem(key, v) } catch { /* ignore */ }
  })
  return store
}

export const navLevel = localWritable('soulacy_nav_level', 'simple')

export const apiKey   = sessionWritable('soulacy_api_key', '')
export const connected = writable(false)  // WebSocket event stream status

// True when the gateway rejected our credentials (401). Distinct from
// "offline": the gateway is reachable but authentication is required.
export const authRequired = writable(false)

// Agent to pre-select when navigating to the Activity page (set by "Watch" buttons).
export const activityAgent = writable('')

// PWA install: holds the deferred beforeinstallprompt event when the app is
// installable (null otherwise), so the UI can offer an explicit Install button.
export const installPrompt = writable(null)
// True once the PWA has been installed to the device.
export const appInstalled = writable(false)

// Agent to pre-select when navigating to the Agents page (set by Studio after save).
export const editAgent = writable('')

// Studio working session — persisted across navigation so the intent, the
// generated/refined workflow, and the transparency panels survive switching to
// another screen and back (the Studio component is destroyed on unmount). Null
// until Studio first saves a snapshot.
export const studioSession = writable(null)

// Activity → Studio handoff: a concrete failed run to debug from the real
// action log. Studio consumes and clears this when opened.
export const studioDebugRun = writable(null)

// Chat page — persisted across navigation so in-flight requests survive unmount.
// chatThreads is keyed by a UI thread id. Each thread owns its agent, runtime
// session, visible messages, branch state, and per-session metrics baseline.
export const chatActiveThreadId = writable('')
export const chatThreads = writable({})

// Legacy single-chat stores kept for older code/tests that import them.
export const chatAgentId  = writable('')
export const chatMessages = writable([])
export const chatSending  = writable(false)
export const chatSessionId = writable(`gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`)
export const chatBranches = writable([])
export const chatBranchMessages = writable({})
export const chatMetricsBaseline = writable({})

// Ask Genie handoff: the floating button anywhere in the app stores the
// question here and navigates to Chat, which starts a thread with the best
// agent, sends it, and clears the store. { text, at } or null.
export const genieAsk = writable(null)
