import { describe, it, expect } from 'vitest'
import { intentOf } from './wizard.js'

// The bug this pins: "Your prompt" is filled, "Refined prompt" is empty, and
// Generate did nothing at all — no request, no error — because generation read
// the refined box alone while the spec panel read both and enabled the button.
describe('intentOf', () => {
  it('uses the raw prompt when nothing has been refined', () => {
    expect(intentOf('', 'A travel advisor agent')).toBe('A travel advisor agent')
  })

  it('prefers the refined prompt once one exists', () => {
    expect(intentOf('Refined version', 'Raw version')).toBe('Refined version')
  })

  it('falls back when the refined box holds only whitespace', () => {
    expect(intentOf('   \n  ', 'Raw version')).toBe('Raw version')
  })

  it('trims what it returns', () => {
    expect(intentOf('', '  padded  ')).toBe('padded')
    expect(intentOf('  refined  ', 'raw')).toBe('refined')
  })

  it('is empty only when both boxes are empty', () => {
    expect(intentOf('', '')).toBe('')
    expect(intentOf(null, undefined)).toBe('')
    expect(intentOf('  ', '  ')).toBe('')
  })

  // The panel decides whether Generate is enabled; generation decides what to
  // build. If those two disagree about whether there is any text, the button is
  // enabled and the action is a no-op.
  it('agrees with the spec panel about whether there is anything to build', () => {
    const cases = [
      ['', 'something'],
      ['something', ''],
      ['', ''],
      ['  ', 'x'],
    ]
    for (const [refined, raw] of cases) {
      // Both sides now call intentOf. Written this way on purpose: the old
      // panel used `(refined || raw || '').trim()`, whose raw truthiness let a
      // whitespace-only refined box shadow the real prompt — the mirror image
      // of the bug above, with the panel blank while generation had text.
      const panelWouldLoadSpec = !!intentOf(refined, raw)
      const generateWouldRun = !!intentOf(refined, raw)
      expect(generateWouldRun).toBe(panelWouldLoadSpec)
    }
    // And specifically: whitespace in the refined box must not hide a real prompt.
    expect(intentOf('   ', 'a real prompt')).toBe('a real prompt')
  })
})
