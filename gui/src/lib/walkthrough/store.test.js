// @vitest-environment jsdom
//
// Walkthrough state: what gets remembered, where, and when.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { get } from 'svelte/store'

const patch = vi.fn(async () => ({ ok: true }))
const getCfg = vi.fn(async () => ({}))
vi.mock('../api.js', () => ({ api: { config: { patch: (...a) => patch(...a), get: (...a) => getCfg(...a) } } }))

const mod = await import('./store.js')
const {
  walkthrough, readLocal, fromConfig, mergeState, loadWalkthroughState,
  startWalkthrough, nextStep, prevStep, gotoStep, pauseWalkthrough,
  skipWalkthrough, finishWalkthrough, shouldAutoStart, resetWalkthrough,
} = mod
const { walkthroughSteps, WALKTHROUGH_VERSION } = await import('./steps.js')

const LAST = walkthroughSteps.length - 1

beforeEach(() => {
  localStorage.clear()
  patch.mockClear()
  getCfg.mockClear()
  getCfg.mockResolvedValue({})
  resetWalkthrough()
})

afterEach(() => { resetWalkthrough() })

describe('merging the two persistence sources', () => {
  it('treats "seen" as sticky across browsers', () => {
    const m = mergeState({ seen: false, step: 0, version: WALKTHROUGH_VERSION },
                         { seen: true, step: 0, version: WALKTHROUGH_VERSION })
    expect(m.seen).toBe(true)
  })

  it('keeps the furthest recorded position', () => {
    const m = mergeState({ seen: false, step: 3, version: 1 }, { seen: false, step: 9, version: 1 })
    expect(m.resumeIndex).toBe(9)
  })

  it('re-arms the tour when the script version outran what was seen', () => {
    // Finished version 0 of the script; the app now ships a newer one.
    const m = mergeState({ seen: true, step: 0, version: 0 }, null)
    expect(m.seen).toBe(false)
  })

  it('survives absent, malformed, and partial input', () => {
    expect(mergeState(null, null).seen).toBe(false)
    expect(fromConfig(null)).toBeNull()
    expect(fromConfig({ ui: 'nope' })).toBeNull()
    expect(fromConfig({ ui: { walkthrough_step: 'seven' } })).toEqual({ seen: false, step: 0, version: 0 })
  })

  it('ignores unparseable localStorage rather than throwing', () => {
    localStorage.setItem('soulacy-walkthrough', '{not json')
    expect(readLocal()).toBeNull()
  })
})

describe('loading', () => {
  it('reads the gateway config block', async () => {
    getCfg.mockResolvedValue({ ui: { walkthrough_seen: true, walkthrough_step: 4, walkthrough_version: WALKTHROUGH_VERSION } })
    const s = await loadWalkthroughState()
    expect(s.loaded).toBe(true)
    expect(s.seen).toBe(true)
    expect(s.resumeIndex).toBe(4)
  })

  it('falls back to localStorage when the gateway will not answer', async () => {
    getCfg.mockRejectedValue(new Error('404'))
    localStorage.setItem('soulacy-walkthrough', JSON.stringify({ seen: true, step: 2, version: WALKTHROUGH_VERSION }))
    const s = await loadWalkthroughState()
    expect(s.seen).toBe(true)
    expect(s.resumeIndex).toBe(2)
  })

  it('auto-starts only on a fresh install, and only once loaded', async () => {
    expect(shouldAutoStart(get(walkthrough))).toBe(false)   // not loaded yet
    const fresh = await loadWalkthroughState()
    expect(shouldAutoStart(fresh)).toBe(true)

    await finishWalkthrough()
    resetWalkthrough()
    const after = await loadWalkthroughState()
    expect(shouldAutoStart(after)).toBe(false)
  })
})

describe('stepping', () => {
  it('walks forward and back within range', () => {
    startWalkthrough(0)
    nextStep(); nextStep()
    expect(get(walkthrough).index).toBe(2)
    prevStep()
    expect(get(walkthrough).index).toBe(1)
    prevStep(); prevStep()
    expect(get(walkthrough).index).toBe(0)
  })

  it('finishes rather than running off the end', () => {
    startWalkthrough(LAST)
    nextStep()
    const s = get(walkthrough)
    expect(s.active).toBe(false)
    expect(s.seen).toBe(true)
  })

  it('resumes where it was left, and can be restarted from the top', () => {
    startWalkthrough(0)
    gotoStep(6)
    pauseWalkthrough()
    expect(get(walkthrough).active).toBe(false)
    expect(get(walkthrough).resumeIndex).toBe(6)

    startWalkthrough('resume')
    expect(get(walkthrough).index).toBe(6)

    gotoStep(0)
    expect(get(walkthrough).index).toBe(0)
  })
})

describe('writing progress back', () => {
  it('does not hit the gateway on every step — only on exit', () => {
    startWalkthrough(0)
    nextStep(); nextStep(); nextStep()
    expect(patch).not.toHaveBeenCalled()
    expect(readLocal().step).toBe(3)
  })

  it('records seen=true on skip, so the tour stops opening itself', async () => {
    startWalkthrough(0)
    nextStep()
    await skipWalkthrough()
    expect(patch).toHaveBeenCalledWith({
      ui: { walkthrough_seen: true, walkthrough_step: 0, walkthrough_version: WALKTHROUGH_VERSION },
    })
    expect(readLocal().seen).toBe(true)
  })

  it('records the position — not seen — when merely paused', async () => {
    startWalkthrough(5)
    await pauseWalkthrough()
    expect(patch).toHaveBeenCalledWith({
      ui: { walkthrough_seen: false, walkthrough_step: 5, walkthrough_version: WALKTHROUGH_VERSION },
    })
  })

  it('still closes cleanly when the gateway rejects the write', async () => {
    patch.mockRejectedValueOnce(new Error('403 forbidden'))
    startWalkthrough(2)
    await expect(skipWalkthrough()).resolves.not.toThrow()
    expect(get(walkthrough).active).toBe(false)
    expect(readLocal().seen).toBe(true)
  })
})
