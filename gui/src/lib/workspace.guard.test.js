// workspace.guard.test.js — the structural half of MU-029 criterion 2:
// "switching clears workspace-specific caches, subscriptions, drafts, and
// optimistic state before loading the destination."
//
// A test that switches and asserts a few stores were cleared proves the stores
// that exist today are handled. It cannot prove the next one will be — and the
// next one is the risk, because a store added after this is written carries
// its workspace's data across a switch and nothing looks wrong until two
// workspaces exist and somebody sees a colleague's draft.
//
// So this reads stores.js as SOURCE and fails when an exported store is
// neither registered as workspace-scoped nor declared workspace-neutral with a
// reason. There is no third answer, and there is no way to add a store without
// choosing one.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { workspaceStateAudit } from './workspace.js'
import * as stores from './stores.js'

const source = readFileSync(fileURLToPath(new URL('./stores.js', import.meta.url)), 'utf8')

// exportedStoreNames finds every `export const <name> =` in stores.js. Crude
// on purpose: the file is a flat list of store declarations, and a real parser
// would be more machinery than the question needs.
function exportedStoreNames(src) {
  return [...src.matchAll(/^export const\s+([A-Za-z0-9_]+)\s*=/gm)].map(m => m[1])
}

describe('every store in stores.js is classified', () => {
  it('finds the stores it is supposed to be checking', () => {
    // Without this the guard passes vacuously the moment the regex stops
    // matching — which is how a source-reading test quietly becomes a no-op.
    const names = exportedStoreNames(source)
    expect(names.length).toBeGreaterThan(10)
    for (const name of names) {
      expect(stores[name], `${name} is exported by the regex but not by the module`).toBeDefined()
    }
  })

  it('classifies each one as workspace-scoped or workspace-neutral, never neither', () => {
    const audit = workspaceStateAudit()
    const classified = new Set([...audit.scoped, ...Object.keys(audit.neutral)])
    const unclassified = exportedStoreNames(source).filter(n => !classified.has(n))
    expect(unclassified, 'a store that is neither scoped nor declared neutral will carry its ' +
      'workspace\'s data across a switch — wrap it in scoped() or neutral() in stores.js').toEqual([])
  })

  it('requires a reason for every workspace-neutral store', () => {
    for (const [name, reason] of Object.entries(workspaceStateAudit().neutral)) {
      expect(String(reason).trim().length, `${name} is exempt with no reason`).toBeGreaterThan(20)
    }
  })

  it('does not classify a store that no longer exists', () => {
    // A stale entry is an exemption waiting to be inherited by whatever takes
    // that name next.
    const names = new Set(exportedStoreNames(source))
    const audit = workspaceStateAudit()
    for (const name of [...audit.scoped, ...Object.keys(audit.neutral)]) {
      expect(names.has(name), `${name} is classified but no longer exported from stores.js`).toBe(true)
    }
  })

  it('keeps the obviously-tenant stores on the scoped side', () => {
    // Named individually as well as counted: these four are the ones whose
    // contents are somebody's work, and a refactor that flipped one to neutral
    // would still satisfy the "everything is classified" check above.
    const scoped = new Set(workspaceStateAudit().scoped)
    for (const name of ['studioSession', 'studioDebugRun', 'chatThreads', 'editAgent']) {
      expect(scoped.has(name), `${name} holds one workspace's work and must not survive a switch`).toBe(true)
    }
  })
})
