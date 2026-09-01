// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { activeWorkspace } from '../lib/workspace.js'
import WorkspaceAdmin from './WorkspaceAdmin.svelte'

let component
let target

function mount() {
  target = document.createElement('div')
  document.body.appendChild(target)
  component = new WorkspaceAdmin({ target })
}

afterEach(() => {
  component?.$destroy()
  target?.remove()
  component = null
  target = null
  activeWorkspace.set(null)
  vi.unstubAllGlobals()
})

describe('workspace administration settings', () => {
  it('does not call management APIs or render controls for a viewer', async () => {
    activeWorkspace.set({ workspaceId: 'ws_one', workspaceName: 'Customer', role: 'viewer' })
    vi.stubGlobal('fetch', vi.fn())
    mount()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(target.textContent).toContain('Administrator access required')
    expect(target.textContent).not.toContain('Create automation credential')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('lets an administrator manage limits without exposing owner-only controls', async () => {
    activeWorkspace.set({ workspaceId: 'ws_one', workspaceName: 'Customer', role: 'admin' })
    vi.stubGlobal('fetch', vi.fn(async url => {
      if (String(url).includes('/workspace/policy')) return Response.json({
        policy: { per_user_daily_tokens: 500000 },
        effective: { per_user_daily_tokens: 500000 },
        retention: {},
      })
      return Response.json({})
    }))
    mount()
    await new Promise(resolve => setTimeout(resolve, 20))

    expect(target.textContent).toContain('Per-user tokens / 24h')
    expect(target.textContent).toContain('Effective per user: 500000')
    expect(target.textContent).not.toContain('Plan & billing')
    expect(target.textContent).not.toContain('Create automation credential')
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('loads owner-scoped policy and management sections', async () => {
    activeWorkspace.set({ workspaceId: 'ws_one', workspaceName: 'Customer', role: 'owner' })
    vi.stubGlobal('fetch', vi.fn(async url => {
      const path = String(url)
      if (path.includes('/workspace/policy')) return Response.json({ policy: { daily_usd: 12 }, effective: { daily_usd: 10 }, retention: {} })
      if (path.includes('/admin/api-keys')) return Response.json({ keys: [] })
      if (path.includes('/admin/audit')) return Response.json({ events: [], next_cursor: '' })
      if (path.includes('/workspace/export')) return Response.json({ exports: [] })
      if (path.includes('/workspace/deletion')) return Response.json({ workspace: { status: 'active', name: 'Customer' } })
      if (path.includes('/billing')) return Response.json({ configured: true, status: 'active', plan: 'team', plans: ['team'] })
      return Response.json({})
    }))
    mount()
    await new Promise(resolve => setTimeout(resolve, 20))

    expect(target.textContent).toContain('Limits & retention')
    expect(target.textContent).toContain('Plan & billing')
    expect(target.textContent).toContain('Effective: $10')
    expect(target.textContent).toContain('Show me around')
    expect(fetch.mock.calls.some(([url]) => String(url).includes('/workspace/policy'))).toBe(true)
  })
})
