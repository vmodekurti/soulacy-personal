// MU-030 criterion 2 (client half) and criterion 5 (the recovery flow).
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { get } from 'svelte/store'
import { permissions, can, activeWorkspace, switchWorkspace } from './workspace.js'
import { apiKey } from './stores.js'
import { apiFetch } from './api.js'

beforeEach(() => {
  permissions.set({})
  activeWorkspace.set(null)
  apiKey.set('')
})

describe('can() is for rendering, not for security', () => {
  it('answers from the map the server served', () => {
    permissions.set({ agents: ['read', 'write'], credentials: ['list'] })
    expect(can('agents', 'write')).toBe(true)
    expect(can('agents', 'delete')).toBe(false)
    expect(can('credentials', 'rotate')).toBe(false)
  })

  it('refuses a resource the map does not mention', () => {
    // Present-but-absent is a real answer: the server enumerated what this
    // role may do, and this was not in it.
    permissions.set({ agents: ['read'] })
    expect(can('secrets', 'set')).toBe(false)
  })

  // Two cases produce an empty map — a personal deployment, which has no roles
  // at all, and the window before identity resolves. Hiding every control in
  // either would break the product for the deployments with no permission
  // problem to solve. Failing open is right precisely because this is not the
  // boundary: the cost is a button that returns 403.
  it('allows everything when the server has said nothing yet', () => {
    expect(can('agents', 'delete')).toBe(true)
    expect(can('anything', 'at-all')).toBe(true)
  })

  it('drops permissions on a workspace switch', async () => {
    // Stale permissions are the most dangerous workspace state to carry: they
    // render an owner's controls in a workspace where the person is a viewer.
    permissions.set({ credentials: ['set', 'delete', 'rotate'] })
    await switchWorkspace({ select: async () => ({ workspace_id: 'ws_b' }) }, 'ws_b')
    expect(get(permissions)).toEqual({})
  })
})

describe('a stale session is re-proved without losing the request', () => {
  function response(status, body = {}, headers = {}) {
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: `HTTP ${status}`,
      headers: { get: (name) => headers[name] || null },
      json: async () => body,
      text: async () => JSON.stringify(body),
    }
  }
  const stale = { code: 'reauthentication_required', error: 'confirm it is still you', max_age_seconds: 600 }

  beforeEach(() => {
    globalThis.fetch = vi.fn()
    globalThis.window = globalThis.window || {}
  })

  it('re-authenticates and retries the identical request once', async () => {
    window.prompt = vi.fn(() => 'the-key')
    fetch
      .mockResolvedValueOnce(response(401, stale))                              // the refusal
      .mockResolvedValueOnce(response(200, { access_token: 'elevated' }))       // reauthenticate
      .mockResolvedValueOnce(response(200, { ok: true }))                       // the retry

    const out = await apiFetch('/workspace/members/m1', { method: 'DELETE' })
    expect(out).toEqual({ ok: true })
    expect(fetch.mock.calls[1][0]).toBe('/api/v1/auth/reauthenticate')
    expect(fetch.mock.calls[2][0]).toBe('/api/v1/workspace/members/m1')
    expect(fetch.mock.calls[2][1].method).toBe('DELETE')
    // The elevated session replaces the current one; retrying with the old
    // token would be refused for the same reason and read as a loop.
    expect(get(apiKey)).toBe('elevated')
  })

  it('retries at most once, so a persistent refusal is not an inescapable prompt', async () => {
    window.prompt = vi.fn(() => 'the-key')
    fetch
      .mockResolvedValueOnce(response(401, stale))
      .mockResolvedValueOnce(response(200, { access_token: 'elevated' }))
      .mockResolvedValueOnce(response(401, stale))

    await expect(apiFetch('/workspace/members/m1', { method: 'DELETE' })).rejects.toMatchObject({ status: 401 })
    expect(window.prompt).toHaveBeenCalledTimes(1)
  })

  it('treats declining as a decision, not as a lost session', async () => {
    const { authRequired } = await import('./stores.js')
    authRequired.set(false)
    window.prompt = vi.fn(() => null)
    fetch.mockResolvedValueOnce(response(401, stale))

    await expect(apiFetch('/workspace/members/m1', { method: 'DELETE' })).rejects.toMatchObject({ status: 401 })
    // Setting authRequired here would log the user out of a session that is
    // still perfectly valid for everything else.
    expect(get(authRequired)).toBe(false)
  })

  it('leaves an ordinary 401 alone', async () => {
    const { authRequired } = await import('./stores.js')
    authRequired.set(false)
    window.prompt = vi.fn(() => 'the-key')
    fetch.mockResolvedValue(response(401, { error: 'invalid or missing API key' }))

    await expect(apiFetch('/agents')).rejects.toMatchObject({ status: 401 })
    expect(window.prompt).not.toHaveBeenCalled()
    expect(get(authRequired)).toBe(true)
  })
})

// The sidebar half of criterion 2. Chrome, not a boundary — but chrome that
// offers a tab which answers 403 on open teaches people to distrust the rest
// of the navigation.
describe('the sidebar shows what the caller can use', () => {
  it('hides destinations the role cannot read', async () => {
    const { visibleNavPages, navPages } = await import('./nav.js')
    permissions.set({ agents: ['read'], chat: ['read', 'chat'] })
    const visible = visibleNavPages(can).map(p => p.id)
    expect(visible).toContain('chat')
    expect(visible).not.toContain('secrets')
    expect(visible).not.toContain('config')
    // A page with no `requires` stays: most screens are read-mostly, and
    // hiding a page nobody is forbidden from is worse than showing it.
    expect(visible).toContain('dashboard')
    expect(visible.length).toBeLessThan(navPages.length)
  })

  it('shows everything in a deployment with no roles (invariant 7)', async () => {
    const { visibleNavPages, navPages } = await import('./nav.js')
    permissions.set({})
    expect(visibleNavPages(can).length).toBe(navPages.length)
  })

  it('shows everything to an owner', async () => {
    const { visibleNavPages, navPages } = await import('./nav.js')
    permissions.set({
      memory: ['read'], knowledge: ['read'], channels: ['read'], schedule: ['read'],
      skills: ['read'], mcp: ['read'], providers: ['read'], secrets: ['list'],
      config: ['read'], logs: ['read'],
    })
    expect(visibleNavPages(can).length).toBe(navPages.length)
  })

  it('every gated entry names a permission the server actually serves', async () => {
    // A typo in `requires` hides the page from everyone, permanently and
    // silently — the failure mode is a screen that simply stopped existing.
    const { navPages } = await import('./nav.js')
    const known = new Set([
      'agents', 'chat', 'approvals', 'memory', 'channels', 'providers', 'skills',
      'mcp', 'knowledge', 'builder', 'templates', 'config', 'logs', 'metrics',
      'schedule', 'rbac', 'secrets', 'credentials',
    ])
    for (const page of navPages) {
      if (!page.requires) continue
      expect(known.has(page.requires[0]), `${page.id} requires unknown resource ${page.requires[0]}`).toBe(true)
      expect(typeof page.requires[1]).toBe('string')
    }
  })
})
