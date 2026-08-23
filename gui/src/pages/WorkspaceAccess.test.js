// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import WorkspaceAccess from './WorkspaceAccess.svelte'

let app
let target

afterEach(() => {
  app?.$destroy()
  target?.remove()
  app = null
  target = null
  history.replaceState({}, '', '/')
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('workspace invitation sign-in', () => {
  it('posts the invitation into OIDC state instead of putting it in the authorization URL', async () => {
    history.replaceState({}, '', '/w/ws_customer?invite=invite-secret')
    vi.stubGlobal('fetch', vi.fn(async (url) => {
      if (String(url).includes('/workspaces/ws_customer/config')) {
        return Response.json({ workspace: { workspace_id: 'ws_customer', workspace_name: 'Customer', organization_name: 'Acme', identity_status: 'active', provider_type: 'google' } })
      }
      return Response.json({ error: 'test stop' }, { status: 400 })
    }))
    target = document.createElement('div')
    document.body.appendChild(target)
    app = new WorkspaceAccess({ target, props: { workspaceID: 'ws_customer' } })
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(document.body.textContent).toContain('invited email address')
    document.querySelector('.signin').click()
    await new Promise((resolve) => setTimeout(resolve, 0))

    const [, options] = fetch.mock.calls.find(([url]) => String(url) === '/api/v1/auth/oidc/start')
    const request = JSON.parse(options.body)
    expect(options.method).toBe('POST')
    expect(request).toMatchObject({ workspace_id: 'ws_customer', invitation_token: 'invite-secret' })
    expect(document.querySelector('.signin').closest('.card').textContent).not.toContain('invitation_token=')
  })

  it('resolves a friendly slug to the canonical workspace ID', async () => {
    history.replaceState({}, '', '/w/customer-support-a1b2c3?invite=invite-secret')
    vi.stubGlobal('fetch', vi.fn(async (url) => {
      if (String(url).includes('/workspaces/customer-support-a1b2c3/config')) {
        return Response.json({ workspace: { workspace_id: 'ws_customer', workspace_slug: 'customer-support-a1b2c3', workspace_name: 'Customer Support', organization_name: 'Acme', identity_status: 'active', provider_type: 'google' } })
      }
      return Response.json({ error: 'test stop' }, { status: 400 })
    }))
    target = document.createElement('div')
    document.body.appendChild(target)
    app = new WorkspaceAccess({ target, props: { workspaceID: 'customer-support-a1b2c3' } })
    await new Promise((resolve) => setTimeout(resolve, 0))
    document.querySelector('.signin').click()
    await new Promise((resolve) => setTimeout(resolve, 0))

    const [, options] = fetch.mock.calls.find(([url]) => String(url) === '/api/v1/auth/oidc/start')
    expect(JSON.parse(options.body).workspace_id).toBe('ws_customer')
  })
})
