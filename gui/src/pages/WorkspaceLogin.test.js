// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import WorkspaceLogin from './WorkspaceLogin.svelte'

let app
let target

afterEach(() => {
  app?.$destroy()
  target?.remove()
  app = null
  target = null
  vi.unstubAllGlobals()
  localStorage.clear()
})

function render() {
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new WorkspaceLogin({ target })
}

describe('workspace discovery', () => {
  it('does not start OAuth before a workspace is known', () => {
    render()

    expect(document.querySelector('form')).not.toBeNull()
    expect(document.body.textContent).toContain('Workspace address')
    expect(document.querySelector('a[href*="oidc/start"]')).toBeNull()
  })

  it('validates a workspace through its public configuration endpoint', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 404 })))
    render()
    const input = document.querySelector('#workspace')
    input.value = 'ws_unknown'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    document.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(fetch).toHaveBeenCalledWith('/api/v1/auth/workspaces/ws_unknown/config')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('could not be found')
  })

  it('shows remembered workspaces without asking users to type again', () => {
    localStorage.setItem('soulacy.recentWorkspaces', JSON.stringify([{ id: 'ws_long_internal_id', slug: 'acme-agents-a1b2c3', name: 'Agents', organization: 'Acme' }]))
    render()

    expect(document.body.textContent).toContain('Recent workspaces')
    expect(document.querySelector('a[href="/w/acme-agents-a1b2c3"]')).not.toBeNull()
    expect(document.body.textContent).not.toContain('ws_long_internal_id')
  })
})
