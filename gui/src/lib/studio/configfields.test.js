import { describe, it, expect } from 'vitest'
import { configFields, applyField, missingFields } from './configfields.js'

describe('configFields', () => {
  it('describes tool fields and flags a missing required tool', () => {
    const f = configFields({ kind: 'tool' })
    const tool = f.find((x) => x.key === 'tool')
    expect(tool.required).toBe(true)
    expect(tool.missing).toBe(true)
  })
  it('suggests an output name from the description', () => {
    const f = configFields({ kind: 'python', description: 'Rank Stocks' })
    const out = f.find((x) => x.key === 'output')
    expect(out.suggestion).toBe('rank_stocks')
  })
  it('reads exit route from params and offers options', () => {
    const f = configFields({ kind: 'exit', params: { route: 'channel' } })
    const route = f[0]
    expect(route.where).toBe('param')
    expect(route.value).toBe('channel')
    expect(route.options).toContain('http')
    expect(route.missing).toBe(false)
  })
})

describe('applyField', () => {
  it('writes a plain field without mutating', () => {
    const node = { kind: 'tool', tool: '' }
    const out = applyField(node, { key: 'tool', where: 'field' }, 'rank_items')
    expect(out.tool).toBe('rank_items')
    expect(node.tool).toBe('')       // original untouched
  })
  it('writes a param field under params', () => {
    const node = { kind: 'exit', params: { route: 'console' } }
    const out = applyField(node, { key: 'route', where: 'param' }, 'channel')
    expect(out.params.route).toBe('channel')
    expect(node.params.route).toBe('console')
  })
})

describe('missingFields', () => {
  it('returns only empty required fields', () => {
    expect(missingFields({ kind: 'tool', tool: '' }).map((f) => f.key)).toEqual(['tool'])
    expect(missingFields({ kind: 'tool', tool: 'x' })).toEqual([])
  })
})
