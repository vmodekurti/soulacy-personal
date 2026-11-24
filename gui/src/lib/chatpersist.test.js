import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { slimThinking, throttleTrailing, tokenBatcher } from './chatpersist.js'

describe('slimThinking', () => {
  it('keeps the latest events, closed, and cuts long tool output', () => {
    const events = Array.from({ length: 200 }, (_, i) => ({ type: 'tool.result', payload: { name: 'web_search', content: 'x'.repeat(i === 199 ? 23000 : 10), n: i } }))
    const slim = slimThinking({ open: true, startedAt: 5, events })
    expect(slim.open).toBe(false)
    expect(slim.startedAt).toBe(5)
    expect(slim.events).toHaveLength(80)
    expect(slim.events[79].payload.n).toBe(199)
    expect(slim.events[79].payload.content.length).toBeLessThan(2100)
    expect(slim.events[79].payload.content).toContain('21000 more characters not kept')
    expect(slim.events[0].payload.content).toBe('x'.repeat(10))
    // The live record is not modified.
    expect(events[199].payload.content).toHaveLength(23000)
    expect(JSON.stringify(slim).length).toBeLessThan(20000)
  })

  it('passes through nothing as nothing', () => {
    expect(slimThinking(null)).toBeNull()
    expect(slimThinking({ open: true }).events).toEqual([])
  })
})

describe('throttleTrailing', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('turns thousands of updates into one save with the latest value', () => {
    const save = vi.fn()
    const throttled = throttleTrailing(save, 1500)
    for (let i = 0; i < 3361; i++) throttled(i)
    expect(save).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1500)
    expect(save).toHaveBeenCalledTimes(1)
    expect(save).toHaveBeenCalledWith(3360)
    vi.advanceTimersByTime(5000)
    expect(save).toHaveBeenCalledTimes(1)
  })

  it('flushes a pending save at once and cancels cleanly', () => {
    const save = vi.fn()
    const throttled = throttleTrailing(save, 1500)
    throttled('a')
    throttled.flush()
    expect(save).toHaveBeenCalledWith('a')
    throttled('b')
    throttled.cancel()
    vi.advanceTimersByTime(5000)
    expect(save).toHaveBeenCalledTimes(1)
    throttled.flush()
    expect(save).toHaveBeenCalledTimes(1)
  })
})

describe('tokenBatcher', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('delivers a burst of tokens as one update per conversation, in order', () => {
    const apply = vi.fn()
    const batch = tokenBatcher(apply, 60)
    'Hello there'.split('').forEach((ch) => batch.push('t1', ch))
    batch.push('t2', 'x')
    batch.push('', 'ignored')
    expect(apply).not.toHaveBeenCalled()
    vi.advanceTimersByTime(60)
    expect(apply).toHaveBeenCalledTimes(2)
    expect(apply).toHaveBeenCalledWith('t1', 'Hello there')
    expect(apply).toHaveBeenCalledWith('t2', 'x')
  })

  it('flush delivers what is waiting before the final reply replaces it', () => {
    const apply = vi.fn()
    const batch = tokenBatcher(apply, 60)
    batch.push('t1', 'par')
    batch.push('t1', 'tial')
    batch.flush()
    expect(apply).toHaveBeenCalledWith('t1', 'partial')
    vi.advanceTimersByTime(500)
    expect(apply).toHaveBeenCalledTimes(1)
  })
})
