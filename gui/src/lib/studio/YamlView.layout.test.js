import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const component = readFileSync(fileURLToPath(new URL('./YamlView.svelte', import.meta.url)), 'utf8')

describe('YamlView layout', () => {
  it('keeps the highlighted pre layer as tall as the textarea', () => {
    const pre = component.match(/\.yv-pre\s*\{([^}]*)\}/)
    expect(pre, 'the YAML highlight layer rule is missing').toBeTruthy()
    expect(pre[1]).toMatch(/max-height:\s*none\s*!important/)
  })
})
