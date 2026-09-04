import { describe, it, expect } from 'vitest'
import { deltaMetrics, deltaLabel } from './chatmetrics.js'

const base = {
  total_tokens: 100, prompt_tokens: 70, comp_tokens: 30,
  cost_usd: 0.001, llm_calls: 1, model: 'gpt-4o-mini', provider: 'openai',
}
const after = {
  total_tokens: 450, prompt_tokens: 300, comp_tokens: 150,
  cost_usd: 0.0045, llm_calls: 3, model: 'gpt-4o', provider: 'openai',
}

describe('deltaMetrics', () => {
  it('computes per-turn deltas against the previous snapshot', () => {
    const d = deltaMetrics(base, after)
    expect(d.tokens).toBe(350)
    expect(d.prompt).toBe(230)
    expect(d.comp).toBe(120)
    expect(d.cost).toBeCloseTo(0.0035, 6)
    expect(d.llmCalls).toBe(2)
    expect(d.model).toBe('gpt-4o')      // model of the latest call
    expect(d.provider).toBe('openai')
    expect(d.cumulative).toBe(after)
  })

  it('treats a missing baseline as zero (first turn)', () => {
    const d = deltaMetrics(null, base)
    expect(d.tokens).toBe(100)
    expect(d.cost).toBeCloseTo(0.001, 6)
  })

  it('returns null when current metrics are unavailable', () => {
    expect(deltaMetrics(base, null)).toBeNull()
  })

  it('never returns negative deltas (e.g. after baseline reset)', () => {
    const d = deltaMetrics(after, base)
    expect(d.tokens).toBe(0)
    expect(d.cost).toBe(0)
  })
})

describe('deltaLabel', () => {
  it('builds the subtle indicator text', () => {
    const d = deltaMetrics(base, after)
    expect(deltaLabel(d)).toBe('+350 tok · $0.0035 · gpt-4o')
  })

  it('omits zero-cost', () => {
    const d = deltaMetrics(null, { ...base, cost_usd: 0 })
    expect(deltaLabel(d)).toBe('+100 tok · gpt-4o-mini')
  })

  it('handles null', () => {
    expect(deltaLabel(null)).toBe('')
  })

  it('returns empty when there was no token movement', () => {
    const d = deltaMetrics(base, { ...base })
    expect(deltaLabel(d)).toBe('')
  })
})
