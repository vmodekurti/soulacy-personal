import { describe, expect, it } from 'vitest'
import fs from 'node:fs'

const source = fs.readFileSync(new URL('./Schedule.svelte', import.meta.url), 'utf8')

describe('automation mobile output', () => {
  it('offers Soulacy Mobile independently of bot settings', () => {
    expect(source).toContain("if (ch.id === 'mobile')")
    expect(source).toContain("Soulacy Mobile'} (all paired devices)")
    expect(source).toContain("if (channelID === 'mobile') editOutputTo = 'all'")
    expect(source).toContain('Results will be retained in the app inbox and pushed to all paired iPhones.')
  })
})
