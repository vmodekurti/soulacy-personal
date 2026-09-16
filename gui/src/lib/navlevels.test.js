import { describe, it, expect } from 'vitest'
import { navPages, navLevels, pagesForLevel } from './nav.js'

// The sidebar had 26 destinations and a first-time user met every one of them
// before doing anything. These pin the shape of the fix.
describe('navigation levels', () => {
  it('gives every page a level, so a new screen cannot skip the decision', () => {
    const valid = new Set(navLevels.map((l) => l.key))
    for (const p of navPages) {
      expect(valid.has(p.level), `${p.id} has level ${p.level}`).toBe(true)
    }
  })

  it('simple is a short list, and the front door is on it', () => {
    const simple = pagesForLevel('simple')
    expect(simple.length).toBeLessThanOrEqual(8)
    expect(simple.map((p) => p.id)).toContain('start')
    expect(simple.map((p) => p.id)).toContain('dashboard')
  })

  it('each level contains everything below it', () => {
    const simple = pagesForLevel('simple').map((p) => p.id)
    const standard = pagesForLevel('standard').map((p) => p.id)
    const advanced = pagesForLevel('advanced').map((p) => p.id)

    for (const id of simple) expect(standard).toContain(id)
    for (const id of standard) expect(advanced).toContain(id)
    expect(advanced.length).toBe(navPages.length)
    expect(standard.length).toBeGreaterThan(simple.length)
  })

  it('keeps sidebar order within a level', () => {
    const order = navPages.map((p) => p.id)
    const shown = pagesForLevel('standard').map((p) => p.id)
    const expected = order.filter((id) => shown.includes(id))
    expect(shown).toEqual(expected)
  })

  it('an unknown level falls back to the smallest list rather than everything', () => {
    expect(pagesForLevel('nonsense').length).toBe(pagesForLevel('simple').length)
  })
})
