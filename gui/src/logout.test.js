// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiKey, authRequired } from './lib/stores.js'

let app = null
let target = null

beforeEach(async () => {
  localStorage.setItem('soulacy-onboarding-seen', '1')
  apiKey.set('session-token')
  authRequired.set(false)
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const path = String(url)
    if (path.endsWith('/auth/logout')) return new Response(null, { status: 204 })
    if (path.endsWith('/auth/oidc/config')) return Response.json({ enabled: true })
    if (path.endsWith('/ping')) return Response.json({ deployment_mode: 'team' })
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
  vi.unstubAllGlobals()
})

describe('logout', () => {
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

  it('shows organization SSO without an API-key form in Team mode', async () => {
    document.querySelector('button[aria-label="Sign out"]').click()
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(document.querySelector('.login-input')).toBeNull()
    expect(document.querySelector('.login-divider')).toBeNull()
    expect(document.querySelector('.login-sso')?.textContent).toContain('Continue with your organization')
    expect(document.querySelector('.login-sub')?.textContent).toContain('Sign in with your organization')
  })
})
