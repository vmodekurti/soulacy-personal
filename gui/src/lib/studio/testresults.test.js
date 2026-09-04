import { describe, it, expect } from 'vitest'
import { stepError, stepResultsByNode, firstFailedNode } from './testresults.js'

const result = {
  trace: [
    { nodeId: 'screen', output: { status: 'ok' }, durationMs: 12 },
    { nodeId: 'rank', error: 'KeyError: price' },
    { nodeId: 'send', output: '{"status":"failed","error":"no chat id"}' },
    { nodeId: 'noop', mocked: true, output: {} },
  ],
}

describe('stepError', () => {
  it('detects direct errors and error-status outputs', () => {
    expect(stepError({ error: 'boom' })).toBe('boom')
    expect(stepError({ output: { status: 'error', error: 'x' } })).toBe('x')
    expect(stepError({ output: { status: 'ok' } })).toBe('')
    expect(stepError({ output: '{"error":"parsed"}' })).toBe('parsed')
  })
})

describe('stepResultsByNode', () => {
  const map = stepResultsByNode(result)
  it('maps each node to pass/fail with detail', () => {
    expect(map.screen.ok).toBe(true)
    expect(map.screen.durationMs).toBe(12)
    expect(map.rank.ok).toBe(false)
    expect(map.rank.error).toMatch(/KeyError/)
    expect(map.send.ok).toBe(false)
    expect(map.noop.mocked).toBe(true)
  })
})

describe('firstFailedNode', () => {
  it('returns the first failing step id', () => {
    expect(firstFailedNode(result)).toBe('rank')
    expect(firstFailedNode({ trace: [{ nodeId: 'a', output: { status: 'ok' } }] })).toBe('')
  })
})
