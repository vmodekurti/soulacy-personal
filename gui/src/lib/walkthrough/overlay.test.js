// @vitest-environment jsdom
//
// The overlay's behaviour: it drives navigation, it advances, and it degrades
// to a centred card when it cannot find the thing it is meant to point at.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { get } from 'svelte/store'
import { tick } from 'svelte'

vi.mock('../api.js', () => ({ api: { config: { patch: async () => ({ ok: true }), get: async () => ({}) } } }))

const { default: Walkthrough } = await import('./Walkthrough.svelte')
const { walkthrough, startWalkthrough, resetWalkthrough } = await import('./store.js')
const { walkthroughSteps } = await import('./steps.js')
const { navAnchor } = await import('../nav.js')

let host = null
let cmp = null
const navigated = []

/** Render the overlay plus a stand-in sidebar carrying the real anchor names. */
function mount({ withAnchors = true } = {}) {
  host = document.createElement('div')
  document.body.appendChild(host)
  if (withAnchors) {
    for (const s of walkthroughSteps.filter((x) => x.anchor)) {
      const b = document.createElement('button')
      b.setAttribute('data-tour', s.anchor)
      host.appendChild(b)
    }
  }
  const target = document.createElement('div')
  host.appendChild(target)
  cmp = new Walkthrough({ target })
  cmp.$on('navigate', (e) => navigated.push(e.detail))
}

const frames = () => new Promise((r) => setTimeout(r, 20))

beforeEach(() => {
  localStorage.clear()
  navigated.length = 0
  resetWalkthrough()
})

afterEach(() => {
  if (cmp) cmp.$destroy()
  if (host) host.remove()
  cmp = null
  host = null
  resetWalkthrough()
})

describe('the overlay', () => {
  it('renders nothing until the tour is started', async () => {
    mount()
    expect(document.querySelector('.wt-card')).toBeNull()
    startWalkthrough(0)
    await tick()
    expect(document.querySelector('.wt-card')).toBeTruthy()
  })

  it('opens on the intro and counts the whole script', async () => {
    mount()
    startWalkthrough(0)
    await frames()
    expect(document.querySelector('.wt-count').textContent)
      .toBe(`Step 1 of ${walkthroughSteps.length}`)
    // The intro is not about a screen, so it must not navigate anywhere.
    expect(navigated).toEqual([])
  })

  it('drives the app to each screen as the tour reaches it', async () => {
    mount()
    startWalkthrough(0)
    await frames()
    document.querySelector('.wt-btn.primary').click()   // → first nav step
    await frames()
    expect(navigated).toEqual(['dashboard'])
    document.querySelector('.wt-btn.primary').click()
    await frames()
    expect(navigated).toEqual(['dashboard', 'onboarding'])
  })

  it('goes back without re-navigating in a loop', async () => {
    mount()
    startWalkthrough(2)          // nav:onboarding
    await frames()
    expect(get(walkthrough).index).toBe(2)
    const back = [...document.querySelectorAll('.wt-btn')].find((b) => b.textContent.trim() === 'Back')
    back.click()
    await frames()
    expect(get(walkthrough).index).toBe(1)
    expect(navigated).toEqual(['onboarding', 'dashboard'])
  })

  it('closes the tour on the final step', async () => {
    mount()
    startWalkthrough(walkthroughSteps.length - 1)
    await frames()
    const done = document.querySelector('.wt-btn.primary')
    expect(done.textContent.trim()).toBe('Done')
    done.click()
    await frames()
    expect(get(walkthrough).active).toBe(false)
    expect(get(walkthrough).seen).toBe(true)
    expect(document.querySelector('.wt-card')).toBeNull()
  })

  it('pauses on Escape, keeping the position for a later resume', async () => {
    mount()
    startWalkthrough(4)
    await frames()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await frames()
    expect(get(walkthrough).active).toBe(false)
    expect(get(walkthrough).seen).toBe(false)
    expect(get(walkthrough).resumeIndex).toBe(4)
  })

  it('steps with the arrow keys', async () => {
    mount()
    startWalkthrough(3)
    await frames()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }))
    await frames()
    expect(get(walkthrough).index).toBe(4)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }))
    await frames()
    expect(get(walkthrough).index).toBe(3)
  })

  it('falls back to a centred card when the anchor is not on screen', async () => {
    mount({ withAnchors: false })
    startWalkthrough(1)          // a step that wants to spotlight the sidebar
    await frames()
    expect(document.querySelector('.wt-card')).toBeTruthy()
    expect(document.querySelector('.wt-spot')).toBeNull()
    expect(document.querySelector('.wt-scrim').className).toContain('plain')
  })

  it('spotlights the anchor when it has a real box', async () => {
    mount()
    const el = document.querySelector(`[data-tour="${navAnchor('studio')}"]`)
    el.getBoundingClientRect = () => ({ top: 120, left: 8, width: 190, height: 34, right: 198, bottom: 154 })
    const idx = walkthroughSteps.findIndex((s) => s.nav === 'studio')
    startWalkthrough(idx)
    await tick()
    // Measure explicitly after Svelte applies the new step. Waiting a fixed
    // number of milliseconds for requestAnimationFrame is flaky under CI load.
    cmp.measure()
    await tick()
    const spot = document.querySelector('.wt-spot')
    expect(spot).toBeTruthy()
    expect(spot.getAttribute('style')).toContain('top: 116px')
    expect(document.querySelector('.wt-scrim').className).not.toContain('plain')
  })
})
