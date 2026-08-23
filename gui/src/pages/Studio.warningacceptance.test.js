import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('./Studio.svelte', import.meta.url)), 'utf8')

describe('Studio warning acknowledgement', () => {
  it('uses an explicit Yes/No choice instead of a free-text justification', () => {
    expect(src).toContain('Are these warnings acceptable?')
    expect(src).toContain('value="yes" bind:group={acceptWarnings}')
    expect(src).toContain('value="no" bind:group={acceptWarnings}')
    expect(src).not.toContain('Why are these warnings acceptable?')
    expect(src).not.toContain('id="accept-why"')
  })

  it('only permits a warning override after Yes is selected', () => {
    expect(src).toContain("acceptWarnings === 'yes'")
    expect(src).toContain('Select Yes to acknowledge these warnings')
  })
})
