import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./markdown.css', import.meta.url)), 'utf8')

// #404: cells inherited `overflow-wrap: anywhere`, which lets a column shrink
// to one character, so "RANK" and "$599.99" were split across lines.
describe('reply tables keep words whole', () => {
  const cellRule = css.match(/\.markdown-body th,\s*\.markdown-body td \{[^}]*\}/)?.[0] || ''

  it('does not let a cell break inside a word', () => {
    expect(cellRule).toContain('word-break: normal;')
    expect(cellRule).toContain('overflow-wrap: break-word;')
    expect(cellRule).not.toContain('anywhere;')
  })

  it('never splits a header label, and still lets a long address wrap', () => {
    expect(css).toContain('.markdown-body th { white-space: nowrap; }')
    expect(css).toContain('.markdown-body td a { overflow-wrap: anywhere; }')
  })
})
