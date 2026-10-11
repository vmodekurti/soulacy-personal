import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const feed = readFileSync(fileURLToPath(new URL('./Feed.svelte', import.meta.url)), 'utf8')

describe('workspace home layout', () => {
  it('starts with Genie and a compact daily overview', () => {
    expect(feed).toContain('What would you like to get done?')
    expect(feed).toContain('class="overview"')
    expect(feed).toContain('Active agents')
    expect(feed).toContain('Needs attention')
    expect(feed).toContain('Saved for later')
  })

  it('uses a focused work stream and useful context rail', () => {
    expect(feed).toContain('grid-template-columns: minmax(0, 1fr) 330px')
    expect(feed).toContain('class="context-rail"')
    expect(feed).toContain('Your team')
    expect(feed).toContain('What just happened')
    expect(feed).not.toContain('min(2200px')
    expect(feed).not.toContain('repeat(auto-fit, minmax(440px')
  })

  it('keeps every result action available without turning home into a card wall', () => {
    expect(feed).toContain('Continue in Genie')
    expect(feed).toContain('Mark unread')
    expect(feed).toContain('Archive')
    expect(feed).toContain('class:library={scope !== \'home\'}')
  })
})
