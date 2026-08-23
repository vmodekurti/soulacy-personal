import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('./Agents.svelte', import.meta.url)), 'utf8')

describe('agent editor defaults', () => {
  it('defaults omitted trigger and enabled fields without overriding false', () => {
    expect(src).toContain("if (!String(editing.trigger || '').trim()) editing.trigger = 'channel'")
    expect(src).toContain('if (editing.enabled == null) editing.enabled = true')
    expect(src).not.toContain('if (!editing.enabled) editing.enabled = true')
  })

  it('uses the workspace-scoped provider and model catalogs in workspace mode', () => {
    expect(src).toContain('workspaceID ? api.workspaceProviders : api.providers')
    expect(src).toContain('workspaceDefaultProvider = res.default_provider ||')
    expect(src).toContain("providerScope = $activeWorkspace?.workspaceId")
    expect(src).toContain('providerScope !== loadedProviderScope && providerScope !== loadingProviderScope')
    expect(src).toContain('if (currentScope !== requestedScope) return')
  })
})
