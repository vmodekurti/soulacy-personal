// workspace.js — the active workspace, and the switch that is allowed to
// change it (MU-029).
//
// THE FAILURE THIS PREVENTS is not "the header shows the wrong name". It is
// that the GUI holds a great deal of workspace-specific state — Studio drafts,
// chat threads, the agent queued for editing, the concurrency versions of
// agents this tab has read — none of which was ever workspace-aware, because
// until now there was one workspace. Switching without clearing leaves one
// tenant's draft on screen inside another tenant's Studio, and leaves this tab
// holding version tokens that belong to agents in a workspace it is no longer
// in. The first is a leak; the second produces conflicts against edits nobody
// made.
//
// REGISTRATION, NOT A CLEAR-EVERYTHING FUNCTION. The obvious design is a
// switchWorkspace() that resets each store by name. That is a list a future
// store is one refactor away from being left off, and being left off is
// invisible until two workspaces exist and somebody notices their colleague's
// draft. So a store declares itself workspace-scoped, the switch iterates the
// registry, and `workspaceStateAudit` (with the guard in
// workspace.guard.test.js) fails the build when a store in stores.js is
// neither scoped nor explicitly declared workspace-neutral with a reason.

import { writable, get } from 'svelte/store'
import { clearVersions } from './resourceversions.js'

const workspaceSelectionKey = 'soulacy.active_workspace'

export function rememberedWorkspaceID() {
  const storage = globalThis?.sessionStorage
  if (!storage) return ''
  try {
    return String(storage.getItem(workspaceSelectionKey) || '').trim()
  } catch (_) {
    return ''
  }
}

export function forgetWorkspaceSelection() {
  const storage = globalThis?.sessionStorage
  if (!storage) return
  try { storage.removeItem(workspaceSelectionKey) } catch (_) {}
}

function rememberWorkspaceSelection(workspaceID) {
  const storage = globalThis?.sessionStorage
  if (!storage) return
  const id = String(workspaceID || '').trim()
  try {
    if (id) storage.setItem(workspaceSelectionKey, id)
    else storage.removeItem(workspaceSelectionKey)
  } catch (_) {}
}

/**
 * activeWorkspace is what the shell renders and what every request is scoped
 * to. A remembered ID is used only as a request-scoping placeholder until the
 * deployment returns the verified workspace identity. With no remembered
 * choice it remains null, so "we have not asked yet" is never rendered as a
 * personal workspace.
 */
const rememberedWorkspace = rememberedWorkspaceID()
export const activeWorkspace = writable(rememberedWorkspace ? { workspaceId: rememberedWorkspace } : null)

/**
 * permissions is what the VERIFIED role may do, as the server reported it
 * (MU-030 criterion 2): `{ agents: ['read','write'], credentials: [...] }`.
 *
 * Served rather than duplicated. A second copy of the RBAC matrix in the
 * client drifts from the one the routes enforce, and the drift is worst in the
 * direction that reads as a server bug — a control offered for something the
 * server refuses. Empty until identity resolves, which is why `can()` has to
 * decide what an unknown answer means.
 */
export const permissions = writable({})

/** selectableWorkspaces is the list the picker offers. */
export const selectableWorkspaces = writable([])

/**
 * workspaceAccessState is the shell's recovery surface (criterion 5).
 *
 * 'ok' | 'empty' | 'suspended' | 'expired' | 'revoked' | 'unavailable'
 *
 * A single store rather than a boolean per case so the shell renders exactly
 * one recovery path, and so a new case cannot be added by setting a flag
 * somewhere and forgetting to render it.
 */
export const workspaceAccessState = writable({ state: 'ok', detail: '', workspaceName: '' })

// ── the registry ────────────────────────────────────────────────────────────

const scoped = new Map() // name -> { store, reset }
const neutral = new Map() // name -> reason

/**
 * registerWorkspaceScoped declares a store whose contents belong to one
 * workspace and must not survive a switch.
 *
 * `reset` is a factory rather than a value: a shared object reset would hand
 * every workspace the same mutable instance, so the second switch would find
 * the first workspace's mutations already in it.
 */
export function registerWorkspaceScoped(name, store, reset) {
  scoped.set(name, { store, reset })
  return store
}

/**
 * declareWorkspaceNeutral records a store that legitimately survives a switch,
 * with the reason. Credentials and UI chrome are the same in every workspace;
 * a draft is not. The reason is required because "this one is fine" is the
 * claim a reviewer needs to check, and an unexplained exemption is how a
 * scoped store ends up on the wrong list.
 */
export function declareWorkspaceNeutral(name, reason) {
  if (!reason || !String(reason).trim()) {
    throw new Error(`workspace-neutral store ${name} must give a reason`)
  }
  neutral.set(name, reason)
}

/** workspaceStateAudit is what the build-time guard reads. */
export function workspaceStateAudit() {
  return { scoped: [...scoped.keys()].sort(), neutral: Object.fromEntries(neutral) }
}

/**
 * clearWorkspaceState resets every registered store.
 *
 * Callers must run this BEFORE loading the destination, never after. Clearing
 * afterwards means the destination's first render happens with the source
 * workspace's drafts on screen — which is the leak, just briefer.
 */
export function clearWorkspaceState() {
  for (const { store, reset } of scoped.values()) {
    store.set(reset())
  }
  // The MU-028 version registry is workspace state too, and the least obvious
  // kind. Agent IDs are unique per workspace, not per deployment, so a token
  // remembered for "support-bot" in one workspace would be sent as the
  // precondition for a DIFFERENT agent of the same name in the next — which
  // the server correctly refuses, producing a conflict against an editor who
  // does not exist. It lives outside stores.js, so the guard cannot see it;
  // this line is why the guard's job is stores.js and not "all state".
  clearVersions()
}

// ── switching ───────────────────────────────────────────────────────────────

/**
 * switchWorkspace moves this tab to another workspace.
 *
 * The order is the whole function:
 *
 *   1. clear   — nothing from the previous workspace survives the transition,
 *                including in the window between the request and its answer.
 *   2. select  — the SERVER decides whether this subject may act there. A
 *                client may request a workspace; only stored membership grants
 *                one, which is why the answer is used rather than the request.
 *   3. load    — pages re-fetch against the verified destination.
 *
 * Clearing first also means a failed switch leaves the tab empty rather than
 * showing the previous workspace's data under the new workspace's name, which
 * is the worst of the three possible outcomes.
 */
export async function switchWorkspace(client, workspaceID) {
  clearWorkspaceState()
  activeWorkspace.set(null)
  // Permissions belong to a membership, so they are workspace state too — and
  // the most dangerous kind to carry across, because stale ones render an
  // owner's controls in a workspace where the person is a viewer.
  permissions.set({})
  try {
    const verified = await client.select(workspaceID)
    const normalized = normalizeWorkspace(verified)
    rememberWorkspaceSelection(normalized?.workspaceId)
    activeWorkspace.set(normalized)
    workspaceAccessState.set({ state: 'ok', detail: '', workspaceName: verified?.workspace_name || '' })
    return verified
  } catch (err) {
    forgetWorkspaceSelection()
    activeWorkspace.set(null)
    workspaceAccessState.set(accessStateFor(err))
    throw err
  }
}

/**
 * resolveDeepLink decides what a tab landing on a URL that names a workspace
 * should do (criterion 3).
 *
 * Returns one of:
 *   { action: 'stay' }                       — no workspace named, or already there
 *   { action: 'switch', workspaceId }        — named, and a membership exists
 *   { action: 'redirect', state, detail }    — named, and it does not
 *
 * **It matches against the membership list, not against the link.** A deep
 * link is attacker-supplied; treating it as a request to be verified rather
 * than as an instruction is the whole point. And the refusal never echoes the
 * workspace name or ID back — that would confirm the existence of a workspace
 * to somebody who is not in it, which is an enumeration oracle wearing an
 * error message (criterion 5).
 */
export function resolveDeepLink(requestedID, memberships, activeID) {
  const requested = String(requestedID || '').trim()
  if (!requested) return { action: 'stay' }
  if (requested === String(activeID || '')) return { action: 'stay' }
  const known = (memberships || []).some(w => w && w.workspace_id === requested)
  if (!known) {
    return {
      action: 'redirect',
      state: 'revoked',
      detail: 'That link points to a workspace you do not have access to. You have been returned to your current workspace.',
    }
  }
  return { action: 'switch', workspaceId: requested }
}

/**
 * accessStateFor maps a failed workspace call onto a recovery path.
 *
 * Every branch produces an actionable sentence and none of them names the
 * resource. "Workspace ws_acme is suspended" tells an ex-member that ws_acme
 * exists and that they were in it; "this workspace is suspended" tells the
 * person who is still in it everything they need.
 */
export function accessStateFor(err) {
  const status = err?.status
  if (status === 401) {
    return { state: 'expired', detail: 'Your session has expired. Sign in again to continue.', workspaceName: '' }
  }
  if (status === 403) {
    return { state: 'revoked', detail: 'Your access to this workspace has been removed. Choose another workspace to continue.', workspaceName: '' }
  }
  if (status === 404) {
    // 404 rather than 403 is the server refusing to confirm existence. The
    // client must not undo that by guessing a friendlier explanation.
    return { state: 'revoked', detail: 'That workspace is not available to you. Choose another workspace to continue.', workspaceName: '' }
  }
  if (status === 423 || err?.body?.status === 'suspended') {
    return { state: 'suspended', detail: 'This workspace is suspended. An owner can restore it from the organization settings.', workspaceName: '' }
  }
  return {
    state: 'unavailable',
    detail: 'The workspace service is unavailable, so no workspace-specific data is being shown. Retry in a moment.',
    workspaceName: '',
  }
}

/**
 * accessStateForList decides what an EMPTY membership list means.
 *
 * Distinct from a failed call: "you belong to no workspaces" is a real,
 * recoverable state with its own path (ask for an invitation), and rendering
 * it as an error sends the user looking for a fault that does not exist.
 */
export function accessStateForList(workspaces) {
  if (!Array.isArray(workspaces) || workspaces.length === 0) {
    return {
      state: 'empty',
      detail: 'You are not a member of any workspace yet. Ask an owner to invite you, then reload.',
      workspaceName: '',
    }
  }
  return { state: 'ok', detail: '', workspaceName: '' }
}

/**
 * workspaceLabel is what the global shell and every destructive dialog show
 * (criteria 1 and 4).
 *
 * Organization AND workspace, because a workspace name is not unique across
 * organizations and "Delete everything in Production" is a different sentence
 * depending on whose Production it is. Falls back to the ID rather than to
 * nothing: an unnamed workspace still has to be identifiable in a dialog that
 * is about to destroy something.
 */
export function workspaceLabel(ws) {
  if (!ws) return ''
  const org = (ws.organizationName || ws.organization_name || '').trim()
  const name = (ws.workspaceName || ws.workspace_name || '').trim() || (ws.workspaceId || ws.workspace_id || '').trim()
  if (!name) return ''
  return org ? `${org} / ${name}` : name
}

/** normalizeWorkspace flattens the server's snake_case onto one client shape. */
export function normalizeWorkspace(raw) {
  if (!raw) return null
  return {
    organizationId: raw.organization_id || raw.organizationId || '',
    organizationName: raw.organization_name || raw.organizationName || '',
    organizationLogo: raw.organization_logo || raw.organizationLogo || '',
    workspaceId: raw.workspace_id || raw.workspaceId || '',
    workspaceName: raw.workspace_name || raw.workspaceName || '',
    workspaceLogo: raw.workspace_logo || raw.workspaceLogo || '',
    membershipId: raw.membership_id || raw.membershipId || '',
    role: raw.role || '',
    deploymentMode: raw.deployment_mode || raw.deploymentMode || '',
  }
}

/**
 * can reports whether the verified role may perform an action.
 *
 * IT IS FOR RENDERING, NEVER FOR SECURITY. Every route authorizes itself; a
 * client that ignored this entirely would see exactly the same refusals. What
 * it buys is a GUI that does not offer buttons that always fail.
 *
 * **Unknown means allowed.** Two cases produce an empty map — a personal
 * deployment, which has no roles at all, and the window before identity
 * resolves — and hiding every control in either would break the product for
 * the deployments that have no permission problem to solve. Failing OPEN is
 * right here precisely because this is not the boundary: the cost of guessing
 * wrong is a button that returns 403, and the cost of guessing wrong the other
 * way is a single-user install with no controls.
 */
export function can(resource, action) {
  const map = get(permissions)
  if (!map || Object.keys(map).length === 0) return true
  const allowed = map[resource]
  if (!Array.isArray(allowed)) return false
  return allowed.includes(action)
}

/** activeWorkspaceId is what the transport stamps on every request. */
export function activeWorkspaceId() {
  const ws = get(activeWorkspace)
  return ws ? ws.workspaceId : ''
}
