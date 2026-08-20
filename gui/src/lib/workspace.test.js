// MU-029 — the switch, the deep link, and the recovery paths.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { get } from 'svelte/store'
import {
  activeWorkspace, workspaceAccessState, switchWorkspace, clearWorkspaceState,
  resolveDeepLink, accessStateFor, accessStateForList, workspaceLabel, normalizeWorkspace,
} from './workspace.js'
import { studioSession, chatThreads, editAgent, apiKey, chatSessionId } from './stores.js'
import { rememberVersions, versionFor, clearVersions } from './resourceversions.js'

beforeEach(() => {
  activeWorkspace.set(null)
  clearVersions()
})

// Criterion 2. The failure is not a stale header — it is one tenant's draft
// rendering inside another tenant's Studio.
describe('switching clears workspace state before loading the destination', () => {
  it('resets drafts, threads and handoffs', async () => {
    studioSession.set({ intent: 'acme quarterly close' })
    chatThreads.set({ t1: { agent: 'acme-bot' } })
    editAgent.set('acme-bot')

    await switchWorkspace({ select: async () => ({ workspace_id: 'ws_b', workspace_name: 'Beta' }) }, 'ws_b')

    expect(get(studioSession)).toBeNull()
    expect(get(chatThreads)).toEqual({})
    expect(get(editAgent)).toBe('')
  })

  it('drops MU-028 version tokens, which belong to the workspace they were read in', async () => {
    // Agent IDs are unique per workspace, so a remembered token for
    // "support-bot" would be sent as the precondition for a DIFFERENT agent of
    // the same name — a conflict against an editor who does not exist.
    rememberVersions({ 'support-bot': '"v1"' })
    expect(versionFor('/agents/support-bot')).toBe('"v1"')

    await switchWorkspace({ select: async () => ({ workspace_id: 'ws_b' }) }, 'ws_b')
    expect(versionFor('/agents/support-bot')).toBe('')
  })

  it('mints a fresh chat session rather than carrying one across', async () => {
    const before = get(chatSessionId)
    await switchWorkspace({ select: async () => ({ workspace_id: 'ws_b' }) }, 'ws_b')
    expect(get(chatSessionId)).not.toBe(before)
    expect(get(chatSessionId)).toBeTruthy()
  })

  it('keeps the credential, which authenticates the person and not the membership', async () => {
    apiKey.set('secret')
    await switchWorkspace({ select: async () => ({ workspace_id: 'ws_b' }) }, 'ws_b')
    expect(get(apiKey)).toBe('secret')
  })

  // The order is the whole function. Clearing after the destination loads
  // means its first render happens with the previous workspace's drafts on
  // screen — the same leak, just briefer.
  it('clears BEFORE the destination is resolved, not after', async () => {
    studioSession.set({ intent: 'acme quarterly close' })
    let draftDuringSelect = 'unset'
    await switchWorkspace({
      select: async () => {
        draftDuringSelect = get(studioSession)
        return { workspace_id: 'ws_b' }
      },
    }, 'ws_b')
    expect(draftDuringSelect).toBeNull()
  })

  // A failed switch must leave the tab empty rather than showing the previous
  // workspace's data under the new workspace's name.
  it('leaves nothing behind when the switch is refused', async () => {
    studioSession.set({ intent: 'acme quarterly close' })
    const denied = Object.assign(new Error('workspace not found'), { status: 404 })
    await expect(switchWorkspace({ select: async () => { throw denied } }, 'ws_x')).rejects.toThrow()
    expect(get(studioSession)).toBeNull()
    expect(get(activeWorkspace)).toBeNull()
    expect(get(workspaceAccessState).state).toBe('revoked')
  })

  it('uses the workspace the server verified, not the one that was asked for', async () => {
    await switchWorkspace({
      select: async () => ({ workspace_id: 'ws_verified', workspace_name: 'Verified' }),
    }, 'ws_requested')
    expect(get(activeWorkspace).workspaceId).toBe('ws_verified')
  })
})

// Criterion 3. A deep link is attacker-supplied; it is a request to verify,
// not an instruction to follow.
describe('deep links are verified against membership', () => {
  const memberships = [{ workspace_id: 'ws_a' }, { workspace_id: 'ws_b' }]

  it('switches when a membership exists', () => {
    expect(resolveDeepLink('ws_b', memberships, 'ws_a')).toEqual({ action: 'switch', workspaceId: 'ws_b' })
  })

  it('stays put when the link names nothing or names where we already are', () => {
    expect(resolveDeepLink('', memberships, 'ws_a').action).toBe('stay')
    expect(resolveDeepLink('ws_a', memberships, 'ws_a').action).toBe('stay')
  })

  it('redirects without confirming the workspace exists', () => {
    const out = resolveDeepLink('ws_secret', memberships, 'ws_a')
    expect(out.action).toBe('redirect')
    // Echoing the id back confirms to a non-member that it is a real
    // workspace — an enumeration oracle wearing an error message.
    expect(out.detail).not.toContain('ws_secret')
  })
})

// Criterion 5. Every state has a recovery path, and none of them names a
// resource the caller cannot reach.
describe('recovery states', () => {
  it('distinguishes expired, revoked, suspended and unavailable', () => {
    expect(accessStateFor({ status: 401 }).state).toBe('expired')
    expect(accessStateFor({ status: 403 }).state).toBe('revoked')
    expect(accessStateFor({ status: 404 }).state).toBe('revoked')
    expect(accessStateFor({ status: 423 }).state).toBe('suspended')
    expect(accessStateFor({ status: 500 }).state).toBe('unavailable')
  })

  it('gives every state something the user can actually do', () => {
    for (const status of [401, 403, 404, 423, 500]) {
      const out = accessStateFor({ status })
      expect(out.detail.length, `status ${status} has no recovery path`).toBeGreaterThan(20)
      expect(out.workspaceName).toBe('')
    }
  })

  it('treats "you belong to nowhere" as its own state, not as an error', () => {
    // Rendering it as a fault sends the user looking for a problem that does
    // not exist; the actual remedy is an invitation.
    expect(accessStateForList([]).state).toBe('empty')
    expect(accessStateForList([]).detail).toMatch(/invite/i)
    expect(accessStateForList([{ workspace_id: 'ws_a' }]).state).toBe('ok')
  })
})

// Criteria 1 and 4 — what the shell and every destructive dialog render.
describe('the workspace label', () => {
  it('names the organization as well as the workspace', () => {
    // "Delete everything in Production" is a different sentence depending on
    // whose Production it is, and workspace names are not unique across orgs.
    expect(workspaceLabel(normalizeWorkspace({
      organization_name: 'Acme', workspace_name: 'Production',
    }))).toBe('Acme / Production')
  })

  it('falls back to the id rather than to nothing', () => {
    // An unnamed workspace still has to be identifiable in a dialog that is
    // about to destroy something.
    expect(workspaceLabel(normalizeWorkspace({ workspace_id: 'ws_a' }))).toBe('ws_a')
  })

  it('renders nothing when there is nothing verified yet', () => {
    // "We have not asked yet" and "you are in your personal workspace" must
    // not look the same.
    expect(workspaceLabel(null)).toBe('')
  })

  it('retains organization and workspace logos from the verified server response', () => {
    expect(normalizeWorkspace({
      organization_logo: 'data:image/png;base64,b3Jn',
      workspace_logo: 'data:image/png;base64,d3M=',
    })).toMatchObject({
      organizationLogo: 'data:image/png;base64,b3Jn',
      workspaceLogo: 'data:image/png;base64,d3M=',
    })
  })
})
