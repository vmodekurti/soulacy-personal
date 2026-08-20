// destructive.guard.test.js — the structural half of MU-029 criterion 4.
//
// Adding the workspace to the dialogs that exist today is the easy half. The
// risk is the next destructive dialog: `confirm('Delete X?')` is one line, it
// reviews cleanly, and it deletes the wrong workspace's X only for the people
// who belong to two. This fails the build on a raw confirm() in a page, so a
// new dialog has to choose between confirmDestructive and confirmLocal.
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { destructivePrompt } from './destructive.js'
import { normalizeWorkspace } from './workspace.js'

const pagesDir = fileURLToPath(new URL('../pages/', import.meta.url))

function pageSources() {
  return readdirSync(pagesDir)
    .filter(name => name.endsWith('.svelte'))
    .map(name => ({ name, src: readFileSync(pagesDir + name, 'utf8') }))
}

// rawConfirms finds `confirm(` that is not one of the two wrappers. The
// negative lookbehind covers `window.confirm(`, `confirmDestructive(`,
// `confirmLocal(` and any identifier ending in `confirm`.
const RAW_CONFIRM = /(?<![A-Za-z0-9_.])confirm\s*\(|window\s*\.\s*confirm\s*\(/g

describe('no page opens a confirmation without classifying it', () => {
  it('is actually reading the pages', () => {
    // Without this the guard passes vacuously if the directory URL breaks —
    // which is how a source-reading test quietly becomes a no-op.
    const pages = pageSources()
    expect(pages.length).toBeGreaterThan(5)
    expect(pages.some(p => p.src.includes('confirmDestructive('))).toBe(true)
  })

  it('leaves no raw confirm() behind', () => {
    const offenders = []
    for (const { name, src } of pageSources()) {
      for (const match of src.matchAll(RAW_CONFIRM)) {
        const line = src.slice(0, match.index).split('\n').length
        offenders.push(`${name}:${line}`)
      }
    }
    expect(offenders,
      'use confirmDestructive() when the action changes workspace-owned state, or ' +
      'confirmLocal() when it only discards something this tab is holding').toEqual([])
  })
})

describe('the destructive prompt names the workspace', () => {
  const team = normalizeWorkspace({
    organization_name: 'Acme', workspace_name: 'Production', deployment_mode: 'team',
  })

  it('adds the organization and workspace on its own line', () => {
    const text = destructivePrompt('Delete agent "support-bot"? This cannot be undone.', team)
    expect(text).toContain('Acme / Production')
    // Its own line: a qualifier buried mid-sentence is the part a person
    // scanning a modal skips.
    expect(text.split('\n\n').length).toBe(2)
    expect(text.split('\n\n')[0]).toBe('Delete agent "support-bot"? This cannot be undone.')
  })

  it('leaves a personal deployment byte-identical (invariant 7)', () => {
    const personal = normalizeWorkspace({ workspace_name: 'Personal', deployment_mode: 'personal' })
    expect(destructivePrompt('Delete agent "x"?', personal)).toBe('Delete agent "x"?')
    expect(destructivePrompt('Delete agent "x"?', null)).toBe('Delete agent "x"?')
  })

  it('still identifies an unnamed workspace', () => {
    // A dialog about to destroy something must be able to say where, even when
    // nobody named the workspace.
    const unnamed = normalizeWorkspace({ workspace_id: 'ws_a', deployment_mode: 'team' })
    expect(destructivePrompt('Delete?', unnamed)).toContain('ws_a')
  })
})
