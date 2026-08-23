import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('./WorkspaceConfig.svelte', import.meta.url)), 'utf8')

describe('workspace configuration scope', () => {
  it('mirrors the Personal-mode controls that are safe to isolate', () => {
    for (const section of [
      'LLM',
      'Studio behavior',
      'Web search',
      'Workspace LLM budgets',
      'Cost estimation',
      'Production SLOs',
      'Workspace profile',
      'Security &amp; runtime',
    ]) expect(src).toContain(section)
  })

  it('keeps deployment-wide controls out of the editable form', () => {
    expect(src).toContain('Intentionally managed by the deployment administrator')
    expect(src).not.toContain('bind:value={pythonPath}')
    expect(src).not.toContain('bind:value={logLevel}')
    expect(src).not.toContain('bind:value={executor')
    expect(src).not.toContain('bind:value={updates')
  })

  it('preserves explicit zero-valued workspace limits when loading', () => {
    expect(src).toContain('cfg.ops?.max_failure_rate ?? 0.1')
    expect(src).toContain('cfg.ops?.max_incomplete_rate ?? 0.05')
    expect(src).toContain('cfg.runtime?.default_max_turns ?? 15')
  })

  it('discovers workspace-scoped providers and their available models', () => {
    expect(src).toContain('api.workspaceProviders.list()')
    expect(src).toContain('api.workspaceProviders.models(providerID)')
    expect(src).toContain('models={modelsByProvider[defaultProvider] || []}')
    expect(src).toContain('on:providerchange')
  })
})
