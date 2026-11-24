// @vitest-environment jsdom
//
// Routing in the chat-first shell.
//
// The shell changes what a route MEANS — #providers is no longer "replace the
// screen with Providers" but "open Providers beside the conversation" — without
// changing a single route name. These cases pin that: the conversation is the
// front door, a known route opens as a panel with the conversation still
// mounted, and closing the panel is just navigating home.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

let app = null
let target = null

async function mountAt(hash) {
  window.location.hash = hash
  const { default: App } = await import('./App.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new App({ target })
  await new Promise((r) => setTimeout(r, 0))
}

const settle = () => new Promise((r) => setTimeout(r, 0))

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })))
  localStorage.clear()
  localStorage.setItem('soulacy-onboarding-seen', '1')
  // Keep the orientation tour closed: while it runs the workspace is drawn.
  localStorage.setItem('soulacy-walkthrough', JSON.stringify({ seen: true, version: 999 }))
})

afterEach(() => {
  if (app) app.$destroy()
  if (target) target.remove()
  app = null
  target = null
  window.location.hash = ''
  vi.unstubAllGlobals()
})

describe('the chat-first shell', () => {
  it('opens on the conversation with no sidebar and no panel', async () => {
    await mountAt('')
    expect(document.querySelector('.focus-top')).not.toBeNull()
    expect(document.querySelector('.sidebar')).toBeNull()
    expect(document.querySelector('.mobile-tabs')).toBeNull()
    expect(document.querySelector('.focus-chat')).not.toBeNull()
    expect(document.querySelector('.focus-panel')).toBeNull()
  })

  it('opens a known route beside the conversation rather than instead of it', async () => {
    await mountAt('#providers')
    const panel = document.querySelector('.focus-panel')
    expect(panel).not.toBeNull()
    expect(panel.getAttribute('aria-label')).toMatch(/providers/i)
    expect(document.querySelector('.focus-chat')).not.toBeNull()
    expect(window.location.hash).toBe('#providers')
  })

  it('closes the panel by going home', async () => {
    await mountAt('#providers')
    document.querySelector('.focus-panel-head button').click()
    await settle()
    expect(document.querySelector('.focus-panel')).toBeNull()
    expect(window.location.hash).toBe('#chat')
  })

  it('sends an unknown route to the conversation and says so in the address bar', async () => {
    await mountAt('#providers')
    window.location.hash = '#totally-not-a-page'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settle()
    expect(document.querySelector('.focus-panel')).toBeNull()
    expect(window.location.hash).toBe('#chat')
  })
})
