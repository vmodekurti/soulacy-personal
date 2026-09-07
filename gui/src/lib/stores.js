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
