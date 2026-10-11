import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('./App.svelte', import.meta.url)), 'utf8')

describe('workspace shell', () => {
  it('keeps the everyday navigation short and moves the complete map into a launcher', () => {
    expect(app).toContain("const railIds = ['feed', 'chat', 'missions', 'schedule', 'agents']")
    expect(app).toContain('aria-label="Primary workspace navigation"')
    expect(app).toContain('aria-label="All Soulacy tools"')
    expect(app).toContain('placeholder="Find a screen"')
    expect(app).toContain('grid-template-columns: 220px minmax(0, 1fr)')
  })

  it('ships day and night themes from the same semantic tokens', () => {
    expect(app).toContain("let theme = 'light'")
    expect(app).toContain(':global(:root[data-theme="dark"])')
    expect(app).toContain('aria-label="Toggle light and dark"')
  })
})
