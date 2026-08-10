// @vitest-environment jsdom
//
// What the app does with a hash that names no screen.
//
// applyHash matched a known route, a retired route, or a path segment, and if
// none of those hit it simply returned. "Simply returned" meant `page` kept its
// previous value: the address bar said #totally-not-a-page while Browser Trace
// stayed on screen, and every in-page control — the "Show me around" button
// most visibly — then spoke about a screen the user was not looking at.
//
// That is reachable without typing nonsense. A bookmark from before a route was
// renamed does it, and so does any link we ship that goes stale.
//
// The rule: the address bar and the screen agree, always. An unknown route
// lands on the dashboard AND rewrites the URL to say so.

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

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })))
  localStorage.clear()
  // Skip the first-run redirect, which would otherwise decide the route for us.
  localStorage.setItem('soulacy-onboarding-seen', '1')
})

afterEach(() => {
  if (app) app.$destroy()
  if (target) target.remove()
  app = null
  target = null
  window.location.hash = ''
  vi.unstubAllGlobals()
})

const activeNavLabel = () => {
  const el = document.querySelector('.nav-item.active, .nav-item[aria-current="page"]')
  return el ? el.textContent.replace(/\s+/g, ' ').trim() : ''
}

describe('a hash that names no screen', () => {
  // Mounting straight onto a bad hash is not the failing case: `page` starts at
  // 'dashboard', so the screen is right by accident and a test that only checks
  // the landing screen passes against the broken code. The bug needs somewhere
  // to be stuck ON — so navigate to a real screen first, then follow the bad
  // link, exactly as a user with a stale bookmark does.
  it('leaves the screen it was on rather than stranding you there', async () => {
    await mountAt('#providers')
    expect(activeNavLabel()).toMatch(/providers/i)

    window.location.hash = '#totally-not-a-page'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await new Promise((r) => setTimeout(r, 0))

    expect(activeNavLabel()).toMatch(/dashboard/i)
    expect(window.location.hash).toBe('#dashboard')
  })

  it('rewrites the address bar, so the URL stops naming a screen that is not shown', async () => {
    await mountAt('#totally-not-a-page')
    expect(window.location.hash).toBe('#dashboard')
  })

  it('leaves a real screen alone', async () => {
    await mountAt('#providers')
    expect(window.location.hash).toBe('#providers')
    expect(activeNavLabel()).toMatch(/providers/i)
  })

  it('leaves a plugin screen alone — those are a prefix, not a list we can enumerate', async () => {
    await mountAt('#plugin:acme')
    expect(window.location.hash).toBe('#plugin:acme')
  })

  it('does not touch a shared conversation link', async () => {
    await mountAt('#share/abc123')
    expect(window.location.hash).toBe('#share/abc123')
  })
})
