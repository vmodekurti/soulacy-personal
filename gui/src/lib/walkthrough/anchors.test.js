// @vitest-environment jsdom
//
// The anchor-resolution test.
//
// Every other check in this folder reads the tour's own data structures, which
// proves the script is internally consistent and nothing else. This one mounts
// the real app shell and asks the only question that matters at runtime: does
// each step's anchor actually exist in the DOM the user sees?
//
// Without it, renaming a nav id or dropping the data-tour attribute produces a
// tour that dims the screen and points at nothing — and no test would notice.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { walkthroughSteps } from './steps.js'
import { navIds, navAnchor } from '../nav.js'

let app = null
let target = null

beforeEach(async () => {
  // App.svelte probes the gateway on mount. Nothing here depends on the
  // answers — we only need the sidebar to render — so every call resolves empty.
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })))
  localStorage.clear()

  const { default: App } = await import('../../App.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new App({ target })
  // Let onMount's promises settle so nothing is mid-render.
  await new Promise((r) => setTimeout(r, 0))
})

afterEach(() => {
  if (app) app.$destroy()
  if (target) target.remove()
  app = null
  target = null
  vi.unstubAllGlobals()
})

const anchoredSteps = walkthroughSteps.filter((s) => s.anchor)

describe('walkthrough anchors resolve against the real app shell', () => {
  it.each(anchoredSteps.map((s) => [s.id, s.anchor]))(
    'step %s finds its anchor %s',
    (_id, anchor) => {
      expect(document.querySelector(`[data-tour="${anchor}"]`)).toBeTruthy()
    },
  )

  it('stamps a tour anchor on every nav button and no stray ones', () => {
    const rendered = [...document.querySelectorAll('[data-tour^="nav:"]')]
      .map((el) => el.getAttribute('data-tour'))
      .sort()
    expect(rendered).toEqual(navIds.map(navAnchor).sort())
  })

  it('renders anchors on elements with a real box, not display:none holders', () => {
    for (const step of anchoredSteps) {
      const el = document.querySelector(`[data-tour="${step.anchor}"]`)
      // jsdom has no layout engine, so getBoundingClientRect is all zeros —
      // what we can assert is that the anchor is a live, attached element the
      // overlay can measure, rather than a detached or hidden node.
      expect(el.isConnected).toBe(true)
      expect(el.hidden).toBe(false)
    }
  })
})
