import { describe, expect, it } from 'vitest'
import { catalogSkillNames, compactCatalog } from './catalog.js'

describe('compactCatalog', () => {
  it('preserves exact MCP names and installed skills for generation', () => {
    const result = compactCatalog({
      tools: {
        builtins: [{ name: 'web_search' }],
        python_tools: [{ name: 'clean_csv' }],
        mcp_tools: [{
          name: 'market_data_get_quote',
          full_name: 'mcp__maverick__market_data_get_quote',
          server: 'maverick',
          description: 'Fetch a quote',
          params: 'ticker*:string',
        }],
      },
      skills: { skills: [{ name: 'market-research', description: 'Research equities' }] },
      agents: { agents: [{ id: 'analyst', name: 'Analyst' }] },
      providers: { providers: { nvidia: {} } },
      channels: { channels: [{ id: 'telegram', enabled: true }, { id: 'slack', enabled: false }] },
    })

    expect(result.tools).toEqual(['clean_csv', 'web_search', 'mcp__maverick__market_data_get_quote'])
    expect(result.mcp[0].server).toBe('maverick')
    expect(result.mcp[0].tools[0].name).toBe('mcp__maverick__market_data_get_quote')
    expect(result.skills).toEqual([{ name: 'market-research', description: 'Research equities' }])
    expect(result.channels).toEqual(['telegram'])
  })
})

describe('catalogSkillNames', () => {
  it('tolerates a loading/error object instead of an inventory array', () => {
    expect(catalogSkillNames({ skills: { error: 'unavailable' } })).toEqual([])
  })
})
