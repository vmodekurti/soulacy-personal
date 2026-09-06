import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('./Agents.svelte', import.meta.url)), 'utf8')

describe('deployed agents mobile layout', () => {
  it('keeps save prominent and prevents the editor action row from overflowing', () => {
    expect(src).toContain('class="hdr-actions page-actions"')
    expect(src).toContain('class="hdr-actions editor-actions"')
    expect(src).toContain('class="btn-primary save-action"')
    expect(src).toContain('.editor-actions .save-action')
    expect(src).toContain('position: sticky;')
    expect(src).toContain('grid-column: 1 / -1;')
  })

  it('turns the agent list into a compact horizontal selector on mobile', () => {
    expect(src).toContain('scroll-snap-type: x proximity;')
    expect(src).toContain('min-width: min(260px, 84vw);')
  })
})
