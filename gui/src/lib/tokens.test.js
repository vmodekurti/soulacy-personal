// @vitest-environment node
//
// Design tokens must resolve to a value.
//
// A bulk conversion of colour literals to tokens rewrote the token
// definitions themselves, producing `--sl-text-faint: var(--sl-text-faint)`.
// Every token then resolved to nothing, text fell back to the inherited
// colour, and whole cards became unreadable on production. Nothing caught it:
// the build succeeded, 956 tests passed, and the page rendered.

import { describe, it, expect } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const appSvelte = fs.readFileSync(path.resolve(__dirname, '../App.svelte'), 'utf8')

// The :root block that defines every token.
const rootBlock = (() => {
  const start = appSvelte.indexOf(':global(:root)')
  expect(start, 'the token block must exist').toBeGreaterThan(-1)
  const open = appSvelte.indexOf('{', start)
  const close = appSvelte.indexOf('}', open)
  return appSvelte.slice(open + 1, close)
})()

const definitions = [...rootBlock.matchAll(/(--[a-z-]+)\s*:\s*([^;]+);/g)]
  .map(([, name, value]) => ({ name, value: value.trim() }))

describe('design tokens', () => {
  it('defines a usable set', () => {
    expect(definitions.length).toBeGreaterThan(5)
  })

  it('never defines a token as itself', () => {
    for (const { name, value } of definitions) {
      expect(value, `${name} points at itself and resolves to nothing`)
        .not.toContain(`var(${name})`)
    }
  })

  it('resolves every token to a concrete value, not another token', () => {
    for (const { name, value } of definitions) {
      // A token may legitimately reference another one, but the chain has to
      // end somewhere inside this block. Nothing here should need indirection.
      expect(value, `${name} = ${value}`).toMatch(/^(#[0-9a-fA-F]{3,8}|rgba?\(|[0-9])/)
    }
  })

  it('has a definition for every token the app uses', () => {
    const declared = new Set(definitions.map((d) => d.name))
    const pages = path.resolve(__dirname, '..')
    const used = new Set()
    const walk = (dir) => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name)
        if (entry.isDirectory()) { walk(full); continue }
        if (!entry.name.endsWith('.svelte')) continue
        const src = fs.readFileSync(full, 'utf8')
        for (const [, name] of src.matchAll(/var\((--sl-[a-z-]+)/g)) used.add(name)
      }
    }
    walk(pages)
    for (const name of used) {
      expect(declared.has(name), `${name} is used but never defined`).toBe(true)
    }
  })
})
