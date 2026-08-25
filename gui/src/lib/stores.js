import { writable } from 'svelte/store'
import { registerWorkspaceScoped, declareWorkspaceNeutral } from './workspace.js'

// MU-029. Every store below is classified: workspace-scoped state is reset
// when this tab moves to another workspace, workspace-neutral state survives
// with the reason recorded. The classification is machine-checked — see
// workspace.guard.test.js — because a new store that is neither is exactly the
// one that carries a draft across a switch, and nobody notices until two
// workspaces exist.
//
// `scoped` and `neutral` wrap `writable` so the declaration sits on the same
// line as the store rather than in a list somewhere else that a rename can
// silently orphan.
function scoped(name, initial) {
  return registerWorkspaceScoped(name, writable(initial()), initial)
}

function neutral(name, reason, store) {
  declareWorkspaceNeutral(name, reason)
  return store
}

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

function sessionJSONWritable(key, initial) {
  let stored = null
  try { stored = JSON.parse(sessionStorage.getItem(key) || 'null') } catch (_) {}
  const store = writable(stored !== null ? stored : initial)
  store.subscribe(val => {
    try {
      if (val == null) sessionStorage.removeItem(key)
      else sessionStorage.setItem(key, JSON.stringify(val))
    } catch (_) {}
  })
  return store
}

export const apiKey = neutral('apiKey',
  'the credential authenticates the SUBJECT, not their membership — one person carries it into every workspace they belong to, and clearing it on a switch would log them out to move rooms',
  sessionWritable('soulacy_api_key', ''))
export const connected = neutral('connected',
  'transport health of the gateway connection; the socket is per tab, not per workspace',
  writable(false))

// True when the gateway rejected our credentials (401/403). Distinct from
// "offline": the gateway is reachable but authentication is required.
export const authRequired = neutral('authRequired',
  'a property of the credential, which is workspace-neutral; resetting it on a switch would hide an active auth failure behind an apparently clean destination',
  writable(false))

// Agent to pre-select when navigating to the Activity page (set by "Watch" buttons).
export const activityAgent = scoped('activityAgent', () => '')

// PWA install: holds the deferred beforeinstallprompt event when the app is
// installable (null otherwise), so the UI can offer an explicit Install button.
export const installPrompt = neutral('installPrompt',
  'a browser-level PWA install offer for the origin; it has nothing to do with which workspace is open',
  writable(null))
// True once the PWA has been installed to the device.
export const appInstalled = neutral('appInstalled',
  'a property of the device, not of a workspace',
  writable(false))

// Agent to pre-select when navigating to the Agents page (set by Studio after save).
export const editAgent = scoped('editAgent', () => '')

// Studio working session — persisted across navigation so the intent, the
// generated/refined workflow, and the transparency panels survive switching to
// another screen and back (the Studio component is destroyed on unmount). Null
// until Studio first saves a snapshot.
// A Studio draft must also survive the full-page OIDC round-trip used to renew
// an expired workspace session. It remains tab-scoped and the workspace reset
// registry clears the value on every workspace switch.
export const studioSession = registerWorkspaceScoped(
  'studioSession', sessionJSONWritable('soulacy_studio_session', null), () => null,
)

// Activity → Studio handoff: a concrete failed run to debug from the real
// action log. Studio consumes and clears this when opened.
export const studioDebugRun = scoped('studioDebugRun', () => null)

// newChatSessionId mints a fresh runtime session. A factory, not a constant:
// a switch must not carry a session id into another workspace, where it would
// address a conversation that does not exist there — and re-using one id for
// two workspaces is exactly the cross-tenant addressing the server-side scope
// spent MU-012 onwards making impossible.
function newChatSessionId() {
  return `gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

// Chat page — persisted across navigation so in-flight requests survive unmount.
// chatThreads is keyed by a UI thread id. Each thread owns its agent, runtime
// session, visible messages, branch state, and per-session metrics baseline.
export const chatActiveThreadId = scoped('chatActiveThreadId', () => '')
export const chatThreads = scoped('chatThreads', () => ({}))

// Legacy single-chat stores kept for older code/tests that import them.
export const chatAgentId = scoped('chatAgentId', () => '')
export const chatMessages = scoped('chatMessages', () => [])
export const chatSending = scoped('chatSending', () => false)
export const chatSessionId = scoped('chatSessionId', newChatSessionId)
export const chatBranches = scoped('chatBranches', () => [])
export const chatBranchMessages = scoped('chatBranchMessages', () => ({}))
export const chatMetricsBaseline = scoped('chatMetricsBaseline', () => ({}))
