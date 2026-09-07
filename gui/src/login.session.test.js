// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { authRequired } from './lib/stores.js'

const json = (status, body = {}) => new Response(JSON.stringify(body), {
  status,
  headers: { 'content-type': 'application/json' },
})

const settle = () => new Promise((resolve) => setTimeout(resolve, 20))

describe('Personal login session handoff', () => {
  let app
  let target
  let loggedIn

  beforeEach(() => {
    loggedIn = false
    authRequired.set(false)
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('soulacy-onboarding-seen', '1')
    window.location.hash = '#dashboard'

    vi.stubGlobal('fetch', vi.fn(async (url) => {
      if (url === '/api/v1/health') return json(200, { status: 'ok', version: 'test' })
      if (url === '/api/v1/auth/token') {
        loggedIn = true
        return json(200, { access_token: 'fresh-session' })
      }
      if (url === '/api/v1/auth/refresh') return json(401, { error: 'expired' })
      if (!loggedIn) return json(401, { error: 'authentication required' })
      if (url === '/api/v1/agents') return json(200, { agents: [] })
      return json(200, {})
    }))
  })

  afterEach(() => {
    app?.$destroy()
    target?.remove()
    app = null
    target = null
    window.location.hash = ''
    vi.unstubAllGlobals()
  })

  it('mounts a clean dashboard after a successful login', async () => {
    const { default: App } = await import('./App.svelte')
    target = document.createElement('div')
    document.body.appendChild(target)
    app = new App({ target })
    await settle()

    expect(document.querySelector('#personal-api-key')).not.toBeNull()
    expect(document.querySelector('.layout')).toBeNull()

    const input = document.querySelector('#personal-api-key')
    input.value = 'deployment-key'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    document.querySelector('#personal-login form').dispatchEvent(new Event('submit', {
      bubbles: true,
      cancelable: true,
    }))
    await settle()

    expect(document.querySelector('#personal-api-key')).toBeNull()
    expect(document.querySelector('.layout')).not.toBeNull()
    expect(document.body.textContent).toContain('Dashboard')
    expect(document.body.textContent).not.toContain('Authentication required — click')
  })
})
