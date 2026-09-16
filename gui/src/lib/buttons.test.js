// @vitest-environment jsdom
//
// Secondary actions have to look pressable.
//
// `.linkish` had no style anywhere in the app, so seventeen buttons across six
// pages rendered as bare text on a dark background. Two of them were reported
// as "not shown as a button" — "Open Studio" and "Change something" — which is
// the correct read: they looked like a sentence.

import { describe, it, expect } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const appSvelte = fs.readFileSync(path.resolve(__dirname, '../App.svelte'), 'utf8')

describe('secondary actions', () => {
  it('styles .linkish globally, since every page relies on it', () => {
    expect(appSvelte).toMatch(/:global\(\.linkish\)/)
  })

  it('gives it an affordance a dark background cannot swallow', () => {
    const block = appSvelte.slice(appSvelte.indexOf(':global(.linkish)'))
    expect(block).toMatch(/text-decoration:\s*underline/)
    expect(block).toMatch(/cursor:\s*pointer/)
    // Inheriting the body colour is what made it read as prose.
    expect(block).toMatch(/color:\s*var\(--sl-accent/)
  })

  it('does not leave an action beside a primary button looking like text', () => {
    const page = fs.readFileSync(path.resolve(__dirname, '../pages/GetStarted.svelte'), 'utf8')
    for (const label of ['Change something', 'Open Studio', 'Build another', 'Not now']) {
      const line = page.split('\n').find((l) => l.includes(`>${label}<`))
      expect(line, `${label} should exist`).toBeTruthy()
      expect(line, `${label} sits next to a primary action and should be a button`).toContain('btn-secondary')
    }
  })
})
