import { describe, it, expect } from 'vitest'
import { humanize, stepLabel, riskLevel, blockReadiness, needsAttention } from './blockmeta.js'

describe('humanize', () => {
  it('title-cases machine names', () => {
    expect(humanize('send_telegram_message')).toBe('Send Telegram Message')
    expect(humanize('run-stock.screener')).toBe('Run Stock Screener')
  })
})

describe('stepLabel', () => {
  it('derives plain-English labels per kind', () => {
    expect(stepLabel({ kind: 'trigger' })).toBe('Trigger')
    expect(stepLabel({ kind: 'exit', params: { route: 'channel' } })).toBe('Channel Delivery')
    expect(stepLabel({ kind: 'tool', tool: 'send_telegram_message' })).toBe('Send Telegram Message')
    expect(stepLabel({ kind: 'agent', agent: 'stock_screener' })).toBe('Run Stock Screener')
    expect(stepLabel({ kind: 'python', description: 'Rank Stocks' })).toBe('Rank Stocks')
    expect(stepLabel({ kind: 'python' })).toBe('Custom Python')
  })
  it('prefers an existing description for tool nodes', () => {
    expect(stepLabel({ kind: 'tool', tool: 'read_skill', description: 'Read skill: math' })).toBe('Read skill: math')
  })
})

describe('riskLevel', () => {
  it('flags external sends as high', () => {
    expect(riskLevel({ kind: 'tool', tool: 'send_telegram_message' })).toBe('high')
    expect(riskLevel({ kind: 'tool', tool: 'rank_items' })).toBe('low')
  })
  it('flags network/host python as high, plain python as medium', () => {
    expect(riskLevel({ kind: 'python', code: 'import requests\nrequests.get(x)' })).toBe('high')
    expect(riskLevel({ kind: 'python', code: 'def run(inputs):\n  return 1' })).toBe('medium')
  })
  it('rates non-console delivery and agents as medium', () => {
    expect(riskLevel({ kind: 'exit', params: { route: 'http' } })).toBe('medium')
    expect(riskLevel({ kind: 'exit', params: { route: 'console' } })).toBe('low')
    expect(riskLevel({ kind: 'agent', agent: 'x' })).toBe('medium')
  })
})

describe('blockReadiness', () => {
  const edges = [{ from: 'a', to: 'b' }]
  it('flags missing required fields as needs-attention', () => {
    expect(blockReadiness({ id: 'b', kind: 'tool' }, { edges }).status).toBe('needs-attention')
    expect(blockReadiness({ id: 'b', kind: 'agent' }, { edges }).missing).toContain('agent')
  })
  it('treats the blank starter code as unwritten', () => {
    const blank = { id: 'b', kind: 'python', code: 'def run(inputs):\n    # TODO\n    return inputs' }
    expect(blockReadiness(blank, { edges }).status).toBe('needs-attention')
  })
  it('marks a wired, complete tool as ready', () => {
    const r = blockReadiness({ id: 'b', kind: 'tool', tool: 'rank_items' }, { edges })
    expect(r.status).toBe('ready')
  })
  it('marks a wired external-send tool as risky (not blocking)', () => {
    const r = blockReadiness({ id: 'b', kind: 'tool', tool: 'send_telegram_message' }, { edges })
    expect(r.status).toBe('risky')
  })
  it('flags a stranded node with no connections', () => {
    const r = blockReadiness({ id: 'z', kind: 'tool', tool: 'rank_items' }, { edges, entry: 'a' })
    expect(r.status).toBe('needs-attention')
    expect(r.reasons.join(' ')).toMatch(/connected/i)
  })
})

describe('needsAttention', () => {
  it('returns only the not-ready nodes', () => {
    const nodes = [
      { id: 'a', kind: 'trigger', params: { kind: 'cron' } },
      { id: 'b', kind: 'tool' },                     // missing tool
      { id: 'c', kind: 'tool', tool: 'rank', },      // stranded (no edges)
    ]
    const out = needsAttention(nodes, { edges: [{ from: 'a', to: 'b' }], entry: 'a' })
    expect(out.map((n) => n.id).sort()).toEqual(['b', 'c'])
  })
})
