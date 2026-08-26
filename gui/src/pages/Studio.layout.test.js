import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./Studio.css', import.meta.url)), 'utf8')

describe('Studio resizable frames', () => {
  it('lets the Inspector fill both its resized and maximized host', () => {
    const child = css.match(/\.insp-host\s+\.inspector\s*\{([^}]*)\}/)
    expect(child, 'the inspector host needs a direct child-width override').toBeTruthy()
    expect(child[1]).toMatch(/flex:\s*1\s+1\s+auto/)
    expect(child[1]).toMatch(/width:\s*100%/)

    const maximized = css.match(/\.body\.max-inspector\s+\.insp-host\s*\{([^}]*)\}/)
    expect(maximized, 'maximized Inspector host rule is missing').toBeTruthy()
    expect(maximized[1]).toMatch(/flex:\s*1\s+1\s+auto/)
  })

  it('does not use Svelte scoped-style syntax in the imported stylesheet', () => {
    // Studio.css is a normal CSS import, not a component <style> block.
    // Leaving :global(...) in it produces an invalid selector in the browser.
    const selectors = css.replace(/\/\*[\s\S]*?\*\//g, '')
    expect(selectors).not.toContain(':global(')
  })

  it('lets the SOUL.yaml editor consume the remaining Studio pane', () => {
    expect(css).toMatch(/\.code-view\s*\{[^}]*flex:\s*1\s+1\s+0[^}]*min-height:\s*0/s)
    expect(css).toMatch(/\.code-editor-wrap\s*\{[^}]*flex:\s*1\s+1\s+0[^}]*min-height:\s*0/s)
    expect(css).toMatch(/\.center\.code-mode\s*\{[^}]*overflow:\s*hidden/s)
  })
})
