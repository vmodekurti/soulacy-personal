// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiKey, authRequired } from './lib/stores.js'
import { activeWorkspace } from './lib/workspace.js'

let app = null
let target = null

beforeEach(async () => {
  localStorage.setItem('soulacy-onboarding-seen', '1')
  apiKey.set('session-token')
  authRequired.set(false)
  activeWorkspace.set({ workspaceId: 'ws-team', deploymentMode: 'team' })
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const path = String(url)
    if (path.endsWith('/auth/logout')) return new Response(null, { status: 204 })
    if (path.endsWith('/auth/oidc/config')) return Response.json({ enabled: true })
    if (path.endsWith('/ping')) return Response.json({ deployment_mode: 'team' })
    if (path.endsWith('/workspace/identity')) return Response.json({
      organization_id: 'org-team', organization_name: 'Team',
      workspace_id: 'ws-team', workspace_name: 'Workspace', deployment_mode: 'team',
      permissions: {},
    })
    if (path.endsWith('/workspace/workspaces')) return Response.json({ workspaces: [] })
    return Response.json({})
  }))

  const { default: App } = await import('./App.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new App({ target })
  await new Promise((resolve) => setTimeout(resolve, 0))
})

afterEach(() => {
  if (app) app.$destroy()
  if (target) target.remove()
  app = null
  target = null
  apiKey.set('')
  authRequired.set(false)
  activeWorkspace.set(null)
  vi.unstubAllGlobals()
})

describe('logout', () => {
  it('does not offer the legacy server API-key control inside a Team workspace', () => {
    expect(document.querySelector('button[title="Set API key"]')).toBeNull()
    expect(document.body.textContent).not.toContain('Authentication required')
  })

  it('does not restore the server API-key control while OIDC workspace context is unresolved', async () => {
    activeWorkspace.set(null)
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(document.querySelector('button[title="Set API key"]')).toBeNull()
  })

  it('is visible in the shell and revokes the current bearer session', async () => {
    const button = document.querySelector('button[aria-label="Sign out"]')
    expect(button).toBeTruthy()

    button.click()
    await new Promise((resolve) => setTimeout(resolve, 0))

    const call = fetch.mock.calls.find(([url]) => String(url).endsWith('/api/v1/auth/logout'))
    expect(call).toBeTruthy()
    expect(call[1].headers.Authorization).toBe('Bearer session-token')
    expect(sessionStorage.getItem('soulacy_api_key')).toBeNull()
    expect(document.querySelector('.login-screen')).toBeTruthy()
  })

  it('shows only workspace and deployment-administrator login choices', async () => {
    document.querySelector('button[aria-label="Sign out"]').click()
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(document.querySelector('.login-input')).toBeNull()
    expect(document.querySelector('.workspace-login-link')?.textContent).toContain('Workspace login')
    expect(document.querySelector('.deployment-admin-link')?.textContent).toContain('Deployment admin login')
    expect(document.querySelector('.workspace-login-link')?.getAttribute('href')).toBe('/workspace-login')
    expect(document.querySelector('.deployment-admin-link')?.getAttribute('href')).toBe('/admin')
  })
})
