// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const src = fs.readFileSync(path.resolve(__dirname, './GenieMark.svelte'), 'utf8')

// The mark has to survive 16px in a sidebar and carry a gradient at 64px on a
// button. These pin the decisions that make both possible.
describe('the Genie mark', () => {
  it('scales from one viewBox rather than shipping sized copies', () => {
    expect(src).toContain('viewBox="0 0 24 24"')
    expect(src).toMatch(/export let size/)
  })

  it('inherits the surrounding colour by default', () => {
    // A nav item dims its icon; a hardcoded fill would stay bright and look
    // like the only thing selected.
    expect(src).toContain('currentColor')
  })

  it('gives each gradient a unique id', () => {
    // Two instances on one page sharing an id makes the second adopt the
    // first, so the button and the dialog would drift apart.
    expect(src).toMatch(/Math\.random/)
  })

  it('stays to three elements, which is what 16px allows', () => {
    const shapes = (src.match(/<(path|circle|rect|polygon)\b/g) || []).length
    expect(shapes).toBeLessThanOrEqual(3)
  })

  it('is hidden from screen readers unless given a title', () => {
    expect(src).toContain('aria-hidden')
    expect(src).toMatch(/export let title/)
  })
})
