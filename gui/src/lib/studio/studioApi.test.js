import { describe, it, expect } from 'vitest'
import { loadCatalogParts } from './studioApi.js'

describe('loadCatalogParts', () => {
  it('returns partial catalog data when one capability source fails', async () => {
    const cat = await loadCatalogParts({
      agents: () => Promise.resolve({ agents: [{ id: 'a' }] }),
      tools: () => Promise.reject(new Error('tool catalog restarting')),
      providers: () => Promise.resolve({ providers: { ollama: {} } }),
      channels: () => Promise.resolve({ channels: [] }),
      skills: () => Promise.resolve({ skills: [] }),
      mcp: () => Promise.resolve({ servers: [{ id: 'deal_intel' }] }),
    }, 1000)

    expect(cat.agents.agents).toHaveLength(1)
    expect(cat.tools.builtins).toEqual([])
    expect(cat.tools.error).toMatch(/tool catalog restarting/)
    expect(cat.mcp.servers[0].id).toBe('deal_intel')
  })

  it('time-boxes a stuck capability source', async () => {
    const cat = await loadCatalogParts({
      agents: () => new Promise(() => {}),
    }, 5)

    expect(cat.agents.agents).toEqual([])
    expect(cat.agents.error).toMatch(/timed out/)
  })
})
