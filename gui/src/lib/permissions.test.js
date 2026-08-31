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

	 it('uses OIDC step-up instead of an API-key prompt when requested', async () => {
	  window.prompt = vi.fn(() => { throw new Error('prompt() is not supported') })
	  window.location = { assign: vi.fn() }
	  fetch
		.mockResolvedValueOnce(response(401, stale))
		.mockResolvedValueOnce(response(200, { enabled: true }))

	  await expect(apiFetch('/workspace/workspaces', {
		method: 'POST', body: JSON.stringify({ name: 'Operations' }),
		_oidcReauthReturnTo: '/admin/setup?resume=workspace-create',
	  })).rejects.toMatchObject({ redirecting: true })
	  expect(window.prompt).not.toHaveBeenCalled()
	  const destination = window.location.assign.mock.calls[0][0]
	  expect(destination).toContain('/api/v1/auth/oidc/start?')
	  expect(destination).toContain('reauthenticate=true')
	  expect(destination).toContain('return_to=%2Fadmin%2Fsetup%3Fresume%3Dworkspace-create')
	})

	it('handles embedded browsers that expose but reject prompt()', async () => {
	  window.prompt = vi.fn(() => { throw new Error('prompt() is not supported') })
	  fetch.mockResolvedValueOnce(response(401, stale))

	  await expect(apiFetch('/workspace/members/m1', { method: 'DELETE' }))
		.rejects.toMatchObject({ status: 401 })
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
    expect(visibleNavPages(can, undefined, { role: 'owner', deploymentMode: 'personal' }).length)
      .toBe(navPages.filter(page => !page.multiUserOnly).length)
  })

  it('shows everything to an owner', async () => {
    const { visibleNavPages, navPages } = await import('./nav.js')
    permissions.set({
      agents: ['read'],
      chat: ['read', 'chat'],
      builder: ['read', 'write'],
      studio: ['read', 'write'],
      memory: ['read'], knowledge: ['read'], channels: ['read'], schedule: ['read'],
      skills: ['read'], mcp: ['read'], providers: ['read'], secrets: ['list'],
      config: ['read'], logs: ['read'], plugins: ['read'],
      rbac: ['read'],
    })
    expect(visibleNavPages(can, undefined, { role: 'owner', deploymentMode: 'personal' }).length)
      .toBe(navPages.filter(page => !page.multiUserOnly).length)
  })

  it('reserves workspace settings for workspace owners', async () => {
    const { visibleNavPages } = await import('./nav.js')
    expect(visibleNavPages(() => true, undefined, { role: 'viewer', deploymentMode: 'team' }).map(page => page.id)).not.toContain('workspace-admin')
    expect(visibleNavPages(() => true, undefined, { role: 'owner', deploymentMode: 'team' }).map(page => page.id)).toContain('workspace-admin')
  })

  it('keeps tenant-safe config and MCP while removing deployment-owned administration', async () => {
    const { visibleNavPages } = await import('./nav.js')
    const pages = visibleNavPages(() => true, undefined, { deploymentMode: 'team' })
    const visible = pages.map(page => page.id)
    expect(visible).toContain('config')
    expect(pages.find(page => page.id === 'config')).toMatchObject({
      label: 'Config', icon: '≡', group: 'system',
    })
    expect(visible).toContain('mcp')
    expect(visible).toContain('pluginmgr')
    expect(visible).toContain('members')
    expect(pages.find(page => page.id === 'providers')).toMatchObject({
      label: 'Providers & models', group: 'integrations',
    })
  })

  it('renders the complete Team workspace sidebar from the server permission projection', async () => {
    const { visibleNavPages } = await import('./nav.js')
    permissions.set({
      agents: ['read'], chat: ['read'], builder: ['write'], studio: ['read', 'write'], memory: ['read'], knowledge: ['read'],
      channels: ['read'], schedule: ['read'], skills: ['read'], mcp: ['read'],
      plugins: ['read'], providers: ['read'], secrets: ['list'], config: ['read'],
      rbac: ['read'],
    })
    const visible = visibleNavPages(can, undefined, { role: 'owner', deploymentMode: 'team' })
      .map(page => page.id)
    expect(visible).toEqual(expect.arrayContaining([
      'studio', 'agents', 'templates', 'chat', 'memory', 'knowledge', 'queues',
      'workboard', 'channels', 'schedule', 'skills', 'mcp', 'pluginmgr',
      'providers', 'secrets', 'activity', 'browser', 'config', 'mobile',
      'members', 'workspace-admin',
    ]))
    expect(visible).not.toContain('logs')
  })

  it('keeps read-only Chat visible but removes the authoring-only Studio destination', async () => {
    const { visibleNavPages } = await import('./nav.js')
    permissions.set({
      agents: ['read'], chat: ['read'], builder: [], channels: ['read'], mcp: ['read'],
    })
    const visible = visibleNavPages(can, undefined, { role: 'viewer', deploymentMode: 'team' })
      .map(page => page.id)
    expect(visible).toContain('chat')
    expect(visible).toContain('channels')
    expect(visible).toContain('mcp')
    expect(visible).not.toContain('studio')
  })

  it('gives operators the runtime surfaces without exposing Studio authoring', async () => {
    const { visibleNavPages } = await import('./nav.js')
    permissions.set({
      agents: ['read', 'enable'], chat: ['read', 'chat'], builder: [],
      approvals: ['read', 'write'], channels: ['read', 'enable'],
      schedule: ['read', 'write'], templates: ['read'],
    })
    const visible = visibleNavPages(can, undefined, { role: 'operator', deploymentMode: 'team' })
      .map(page => page.id)
    expect(visible).toContain('chat')
    expect(visible).toContain('agents')
    expect(visible).toContain('schedule')
    expect(visible).toContain('channels')
    expect(visible).not.toContain('studio')
  })

  it('gives public-demo developers a read-rich tour without administration pages', async () => {
    const { visibleNavPages } = await import('./nav.js')
    permissions.set({
      agents: ['read'], chat: ['read', 'chat'], studio: ['read', 'write'],
      providers: ['read'], templates: ['read'], mcp: ['read'], channels: ['read'],
      skills: ['read'], plugins: ['read'], knowledge: ['read'], schedule: ['read'],
    })
    const visible = visibleNavPages(can, undefined, { role: 'demo_developer', deploymentMode: 'team' })
      .map(page => page.id)
    expect(visible).toContain('studio')
    expect(visible).toContain('chat')
    expect(visible).toContain('agents')
    expect(visible).toContain('templates')
    expect(visible).toContain('mcp')
    expect(visible).toContain('channels')
    expect(visible).not.toContain('secrets')
    expect(visible).not.toContain('members')
    expect(visible).not.toContain('workspace-admin')
  })

  it('treats Personal mode as the Team/Scale baseline', async () => {
    const { visibleNavPages, navPages } = await import('./nav.js')
    const personal = visibleNavPages(() => true, undefined, { role: 'owner', deploymentMode: 'personal' })
      .map(page => page.id)
    const team = visibleNavPages(() => true, undefined, { role: 'owner', deploymentMode: 'team' })
      .map(page => page.id)
    const personalOnly = navPages.filter(page => page.personalOnly).map(page => page.id)
    for (const id of personal.filter(id => !personalOnly.includes(id))) expect(team).toContain(id)
    expect(personal.filter(id => !team.includes(id))).toEqual(personalOnly)
    expect(team.filter(id => !personal.includes(id))).toEqual(['members', 'workspace-admin'])
  })

  it('every gated entry names a permission the server actually serves', async () => {
    // A typo in `requires` hides the page from everyone, permanently and
    // silently — the failure mode is a screen that simply stopped existing.
    const { navPages } = await import('./nav.js')
    const known = new Set([
      'agents', 'chat', 'approvals', 'memory', 'channels', 'providers', 'skills',
      'mcp', 'knowledge', 'builder', 'studio', 'templates', 'config', 'logs', 'metrics',
      'schedule', 'rbac', 'secrets', 'credentials', 'plugins',
    ])
    for (const page of navPages) {
      if (!page.requires) continue
      expect(known.has(page.requires[0]), `${page.id} requires unknown resource ${page.requires[0]}`).toBe(true)
      expect(typeof page.requires[1]).toBe('string')
    }
  })
})
