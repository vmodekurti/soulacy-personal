// Parity between the sidebar and the tour script.
//
// A product tour rots the same way every seam in this codebase rots: someone
// adds a screen on one side and nobody updates the other. These tests make that
// a build failure instead of a silent gap in the tour.

import { describe, it, expect } from 'vitest'
import { navPages, navIds, navAnchor } from '../nav.js'
import { walkthroughSteps, navTourCopy, clampIndex } from './steps.js'

describe('walkthrough script', () => {
  it('covers every nav destination — a new screen must be toured', () => {
    const missing = navIds.filter((id) => !navTourCopy[id])
    expect(missing, `nav entries with no tour copy: ${missing.join(', ')}`).toEqual([])
  })

  it('has no copy for screens that no longer exist in the nav', () => {
    const orphans = Object.keys(navTourCopy).filter((id) => !navIds.includes(id))
    expect(orphans, `tour copy for unknown nav ids: ${orphans.join(', ')}`).toEqual([])
  })

  it('visits screens in the order the sidebar lists them', () => {
    const toured = walkthroughSteps.filter((s) => s.nav).map((s) => s.nav)
    expect(toured).toEqual(navIds)
  })

  it('is bookended by a centred intro and outro', () => {
    expect(walkthroughSteps[0].place).toBe('center')
    expect(walkthroughSteps[walkthroughSteps.length - 1].place).toBe('center')
    expect(walkthroughSteps).toHaveLength(navPages.length + 2)
  })

  it('gives every step a title and something to say', () => {
    for (const s of walkthroughSteps) {
      expect(s.title, `step ${s.id} has no title`).toBeTruthy()
      expect((s.what || '').length, `step ${s.id} has no body`).toBeGreaterThan(20)
    }
  })

  it('derives anchors through navAnchor rather than hand-written selectors', () => {
    for (const s of walkthroughSteps.filter((x) => x.nav)) {
      expect(s.anchor).toBe(navAnchor(s.nav))
    }
  })

  it('has unique step ids', () => {
    const ids = walkthroughSteps.map((s) => s.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('clamps a stale persisted index into range', () => {
    expect(clampIndex(-5)).toBe(0)
    expect(clampIndex(9999)).toBe(walkthroughSteps.length - 1)
    expect(clampIndex('3')).toBe(3)
    expect(clampIndex(undefined)).toBe(0)
    expect(clampIndex(NaN)).toBe(0)
  })
})

// The tour covers every destination, but the sidebar shows a subset depending
// on the chosen level. A stop for a screen that is not listed would dim the
// page and point at nothing, so the runtime skips it — and that skip has to be
// wired to the real nav, not silently defaulted on by a swallowed import error.
describe('tour stops follow the visible sidebar', () => {
  it('every nav step names a page that exists at some level', async () => {
    const { navPages } = await import('../nav.js')
    const ids = new Set(navPages.map((p) => p.id))
    for (const step of walkthroughSteps.filter((s) => s.nav)) {
      expect(ids.has(step.nav), `step ${step.id} points at a page that no longer exists`).toBe(true)
    }
  })

  it('simple mode hides most stops but keeps the essentials', async () => {
    const { pagesForLevel } = await import('../nav.js')
    const simple = new Set(pagesForLevel('simple').map((p) => p.id))
    const navSteps = walkthroughSteps.filter((s) => s.nav)
    const shown = navSteps.filter((s) => simple.has(s.nav))
    expect(shown.length).toBeGreaterThan(0)
    expect(shown.length).toBeLessThan(navSteps.length)
    // Get Started is the whole point of simple mode; it must be toured.
    expect(shown.some((s) => s.nav === 'start')).toBe(true)
  })
})
