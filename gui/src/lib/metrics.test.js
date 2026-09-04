import { describe, it, expect } from 'vitest'
import { fmtTokens, fmtCost, fmtDuration, metricParts } from './metrics.js'

describe('fmtTokens', () => {
  it('formats plain, thousands, millions', () => {
    expect(fmtTokens(0)).toBe('0')
    expect(fmtTokens(999)).toBe('999')
    expect(fmtTokens(1234)).toBe('1.2k')
    expect(fmtTokens(15000)).toBe('15k')
    expect(fmtTokens(2_500_000)).toBe('2.5M')
  })
  it('handles missing values', () => {
    expect(fmtTokens(null)).toBe('–')
    expect(fmtTokens(undefined)).toBe('–')
  })
})

describe('fmtCost', () => {
  it('formats zero, sub-cent, and dollar amounts', () => {
    expect(fmtCost(0)).toBe('$0')
    expect(fmtCost(0.00432)).toBe('$0.0043')
    expect(fmtCost(1.5)).toBe('$1.50')
    expect(fmtCost(0.05)).toBe('$0.05')
  })
  it('handles missing values', () => {
    expect(fmtCost(null)).toBe('–')
  })
})

describe('fmtDuration', () => {
  it('formats ms, seconds, minutes', () => {
    expect(fmtDuration(950)).toBe('950ms')
    expect(fmtDuration(8000)).toBe('8s')
    expect(fmtDuration(8500)).toBe('8.5s')
    expect(fmtDuration(130000)).toBe('2m 10s')
    expect(fmtDuration(120000)).toBe('2m')
  })
  it('handles missing/negative', () => {
    expect(fmtDuration(null)).toBe('–')
    expect(fmtDuration(-5)).toBe('–')
  })
})

describe('metricParts', () => {
  it('builds the full strip', () => {
    const parts = metricParts({
      provider: 'openai', model: 'gpt-4o', duration_ms: 8000,
      total_tokens: 430, prompt_tokens: 300, comp_tokens: 130,
      cost_usd: 0.005, tool_calls: 2,
    })
    expect(parts).toEqual([
      'openai/gpt-4o', '8s', '430 tok (300↑ 130↓)', '$0.0050', '2 tools',
    ])
  })
  it('omits parts without data', () => {
    expect(metricParts({ total_tokens: 0, tool_calls: 0, cost_usd: 0 })).toEqual([])
    expect(metricParts(null)).toEqual([])
    expect(metricParts({ tool_calls: 1 })).toEqual(['1 tool'])
  })
})
