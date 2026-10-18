import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('./App.svelte', import.meta.url)), 'utf8')
const websiteAccess = readFileSync(fileURLToPath(new URL('./pages/WebsiteAccess.svelte', import.meta.url)), 'utf8')

function tokenBlock(pattern) {
  const match = app.match(pattern)
  expect(match, `missing theme block ${pattern}`).toBeTruthy()
  return Object.fromEntries(
    [...match[1].matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})\s*;/gi)].map((entry) => [entry[1], entry[2]])
  )
}

function channel(value) {
  value /= 255
  return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
}

function luminance(hex) {
  const rgb = [1, 3, 5].map((start) => Number.parseInt(hex.slice(start, start + 2), 16))
  return 0.2126 * channel(rgb[0]) + 0.7152 * channel(rgb[1]) + 0.0722 * channel(rgb[2])
}

function contrast(foreground, background) {
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a)
  return (values[0] + 0.05) / (values[1] + 0.05)
}

function expectReadable(tokens, foreground, background, minimum = 4.5) {
  const ratio = contrast(tokens[foreground], tokens[background])
  expect(ratio, `${foreground} on ${background} is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(minimum)
}

describe('workspace color contrast', () => {
  const light = tokenBlock(/:global\(:root\) \{\n([\s\S]*?)\n  \}/)
  const dark = tokenBlock(/:global\(:root\[data-theme="dark"\]\) \{\n([\s\S]*?)\n  \}/)

  for (const [name, tokens] of [['light', light], ['dark', dark]]) {
    it(`${name} theme keeps text readable on its common surfaces`, () => {
      expectReadable(tokens, 'sl-text', 'sl-bg')
      expectReadable(tokens, 'sl-text', 'sl-surface')
      expectReadable(tokens, 'sl-text-dim', 'sl-surface')
      expectReadable(tokens, 'sl-text-faint', 'sl-surface-raised')
      expectReadable(tokens, 'sl-accent-ink', 'sl-surface')
    })

    it(`${name} theme keeps controls and primary actions distinguishable`, () => {
      expectReadable(tokens, 'sl-on-accent', 'sl-accent-fill')
      expectReadable(tokens, 'sl-on-accent', 'sl-accent-fill-hover')
      expectReadable(tokens, 'sl-line-strong', 'sl-surface', 3)
    })
  }

  it('keeps authenticated website cards on the shared theme surfaces', () => {
    expect(websiteAccess).toContain('.card, .connection, .guardrail, .modal {')
    expect(websiteAccess).toContain('background: var(--sl-surface)')
    expect(websiteAccess).toContain('.facts span { color: var(--sl-text-dim)')
    expect(websiteAccess).toContain('.primary { color: var(--sl-on-accent); background: var(--sl-accent-fill)')
  })
})
