import { describe, expect, it } from 'vitest'
import fs from 'node:fs'

const source = fs.readFileSync(new URL('./Channels.svelte', import.meta.url), 'utf8')

describe('minimal channel cards', () => {
  it('keeps operational detail collapsed until requested', () => {
    expect(source).toContain('let expandedCards = {}')
    expect(source).toContain('{#if expandedCards[ch.id]}')
    expect(source).toContain("{expandedCards[ch.id] ? 'Less' : 'Details'}")
    expect(source).toContain('aria-expanded={!!expandedCards[ch.id]}')
  })

  it('uses a bounded desktop grid and one column on phones', () => {
    expect(source).toContain('grid-template-columns: repeat(4, minmax(0, 1fr))')
    expect(source).toContain('.channel-grid { grid-template-columns: 1fr; }')
  })
})
