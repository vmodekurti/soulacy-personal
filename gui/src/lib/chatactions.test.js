import { describe, it, expect } from 'vitest'
import {
  filterThreads, suggestedPrompts, buildOverrides,
  lastUserText, truncateForRerun, isLongOutput,
} from './chatactions.js'

const name = (id) => ({ a: 'Alpha', b: 'Bravo' }[id] || id)

describe('filterThreads', () => {
  const threads = [
    { id: '1', agentId: 'a', title: 'Taxes', updatedAt: 10 },
    { id: '2', agentId: 'b', title: 'Travel', updatedAt: 30, pinned: true },
    { id: '3', agentId: 'a', title: 'Old', updatedAt: 5, archived: true },
    { id: '4', agentId: 'b', title: 'Trip planning', updatedAt: 20 },
  ]
  it('hides archived by default, pinned first then recency', () => {
    const r = filterThreads(threads, '', false, name)
    expect(r.map((t) => t.id)).toEqual(['2', '4', '1'])
  })
  it('includes archived when requested', () => {
    expect(filterThreads(threads, '', true, name).some((t) => t.id === '3')).toBe(true)
  })
  it('searches title and agent name', () => {
    expect(filterThreads(threads, 'tr', false, name).map((t) => t.id).sort()).toEqual(['2', '4'])
    expect(filterThreads(threads, 'bravo', false, name).map((t) => t.id).sort()).toEqual(['2', '4'])
  })
})

describe('suggestedPrompts', () => {
  it('pulls from agent fields and dedupes', () => {
    const a = { suggested_prompts: ['Find flights', 'Find flights'], actions: [{ label: 'Book a hotel' }] }
    expect(suggestedPrompts(a)).toEqual(['Find flights', 'Book a hotel'])
  })
  it('falls back to defaults and respects the limit', () => {
    const r = suggestedPrompts(null, 2)
    expect(r.length).toBe(2)
    expect(r[0]).toMatch(/help/i)
  })
})

describe('buildOverrides', () => {
  it('returns null when empty', () => {
    expect(buildOverrides({})).toBeNull()
    expect(buildOverrides({ provider: '  ', model: '' })).toBeNull()
  })
  it('builds an llm override with only set fields and numeric coercion', () => {
    expect(buildOverrides({ provider: 'ollama', model: 'qwen3:32b', temperature: '0.4', topP: '0.8', maxTokens: '2048', responseFormat: 'json', reasoningEffort: 'high' }))
      .toEqual({ llm: { provider: 'ollama', model: 'qwen3:32b', temperature: 0.4, top_p: 0.8, max_tokens: 2048, response_format: 'json', reasoning_effort: 'high' } })
  })
  it('ignores zero/blank max_tokens and NaN temperature', () => {
    expect(buildOverrides({ model: 'x', maxTokens: '0', temperature: 'abc' })).toEqual({ llm: { model: 'x' } })
  })
})

describe('lastUserText / truncateForRerun', () => {
  const msgs = [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: 'hello' },
    { role: 'user', text: 'find flights' },
    { role: 'assistant', text: 'sure' },
  ]
  it('lastUserText finds the most recent user message', () => {
    expect(lastUserText(msgs)).toBe('find flights')
  })
  it('truncateForRerun keeps everything before the index', () => {
    expect(truncateForRerun(msgs, 2).map((m) => m.text)).toEqual(['hi', 'hello'])
    expect(truncateForRerun(msgs, 0)).toEqual([])
  })
})

describe('isLongOutput', () => {
  it('flags long text and many lines', () => {
    expect(isLongOutput('x'.repeat(2000))).toBe(true)
    expect(isLongOutput(Array(40).fill('line').join('\n'))).toBe(true)
    expect(isLongOutput('short')).toBe(false)
  })
})
