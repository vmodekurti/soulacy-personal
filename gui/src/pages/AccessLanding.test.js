// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import AccessLanding from './AccessLanding.svelte'

let app
let target

afterEach(() => {
  app?.$destroy()
  target?.remove()
  app = null
  target = null
  history.replaceState({}, '', '/')
})

function render(props = { oidcEnabled: true, discoveryComplete: true }) {
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new AccessLanding({ target, props })
}

describe('public access landing', () => {
  it('offers only workspace and deployment-administrator access', () => {
    render()

    expect(document.querySelector('input')).toBeNull()
    expect(document.querySelector('.workspace-login-link')?.getAttribute('href')).toBe('/workspace-login')
    expect(document.querySelector('.deployment-admin-link')?.getAttribute('href')).toBe('/admin')
  })

  it('offers self-service workspace creation only when enabled', () => {
    render({ signupEnabled: true })
    expect(document.querySelector('.signup-link')?.getAttribute('href')).toBe('/signup')
    app.$destroy()
    target.innerHTML = ''
    app = new AccessLanding({ target, props: { signupEnabled: false } })
    expect(document.querySelector('.signup-link')).toBeNull()
  })

  it('turns a safe OAuth error code into an actionable message', () => {
    history.replaceState({}, '', '/?auth_error=not_authorized')
    render()

    expect(document.querySelector('[role="alert"]')?.textContent).toContain('not authorized')
    expect(document.body.textContent).not.toContain('authentication failed')
  })
})
