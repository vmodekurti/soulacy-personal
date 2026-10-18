import { describe, expect, it, vi } from 'vitest'
import { detachedChatOutcome, isChatTransportError, recoverDetachedChat } from './chatrecovery.js'

describe('chat transport recovery', () => {
  it('recognizes browser transport errors but not HTTP failures', () => {
    expect(isChatTransportError(new Error('Load failed'))).toBe(true)
    expect(isChatTransportError(new Error('Failed to fetch'))).toBe(true)
    expect(isChatTransportError(new TypeError(''))).toBe(true)
    expect(isChatTransportError(Object.assign(new Error('Bad Gateway'), { status: 502 }))).toBe(false)
  })

  it('recovers only the assistant response paired with the submitted turn', () => {
    const result = detachedChatOutcome([
      { role: 'user', agent_id: 'a', content: 'older' },
      { role: 'assistant', agent_id: 'a', content: 'old answer' },
      { role: 'user', agent_id: 'a', content: 'latest query' },
      { role: 'assistant', agent_id: 'a', content: 'recovered answer' },
    ], [], { agentId: 'a', sentText: 'latest query' })
    expect(result).toMatchObject({ status: 'success', reply: 'recovered answer' })
  })

  it('returns the durable provider error after a detached run fails', () => {
    const result = detachedChatOutcome([], [{
      type: 'run.completed', agent_id: 'a', timestamp: '2026-09-01T14:12:53Z',
      payload: { success: false, error: 'engine: llm timeout for provider "nvidia"' },
    }], { agentId: 'a', startedAt: Date.parse('2026-09-01T14:09:52Z') })
    expect(result).toEqual({ status: 'error', error: 'engine: llm timeout for provider "nvidia"' })
  })

  it('polls until the detached response is persisted', async () => {
    let calls = 0
    const sleep = vi.fn(async () => {})
    const result = await recoverDetachedChat({
      agentId: 'a', sentText: 'question', startedAt: 1,
      now: () => calls * 10,
      timeoutMs: 100,
      pollMs: 10,
      sleep,
      loadEvents: async () => ({ events: [] }),
      loadHistory: async () => {
        calls++
        return calls < 2 ? { entries: [] } : { entries: [
          { role: 'user', agent_id: 'a', content: 'question' },
          { role: 'assistant', agent_id: 'a', content: 'answer' },
        ] }
      },
    })
    expect(result).toMatchObject({ status: 'success', reply: 'answer' })
    expect(sleep).toHaveBeenCalledOnce()
  })
})


describe('recovery boundaries', () => {
  it('ignores another chat and recoverable tool errors', () => {
    const options = { agentId: 'a', sessionId: 'current' }
    expect(detachedChatOutcome([], [
      { type: 'error', agent_id: 'a', session_id: 'current', payload: { error: 'retrying' } },
      { type: 'run.completed', agent_id: 'a', session_id: 'other', payload: { success: false } },
    ], options)).toEqual({ status: 'pending' })
  })
  it('does not reuse an older answer to an identical question', () => {
    expect(detachedChatOutcome([
      { id: 'old-user', role: 'user', content: 'again' },
      { id: 'old-answer', role: 'assistant', content: 'outdated' },
    ], [], { sentText: 'again', baselineEntryIds: ['old-user', 'old-answer'] })).toEqual({ status: 'pending' })
  })
  it('does not pair a pending turn with a later turn’s answer', () => {
    expect(detachedChatOutcome([
      { role: 'user', content: 'first' }, { role: 'user', content: 'second' },
      { role: 'assistant', content: 'second answer' },
    ], [], { sentText: 'first' })).toEqual({ status: 'pending' })
  })
  it('preserves incomplete status when recovering a partial answer', () => {
    const result = detachedChatOutcome([{ role: 'user', content: 'go' }, { role: 'assistant', content: 'partial work' }], [
      { type: 'run.completed', agent_id: 'a', session_id: 's', payload: { success: false, outcome: 'incomplete', summary: 'Turn limit reached' } },
    ], { agentId: 'a', sessionId: 's', sentText: 'go' })
    expect(result).toMatchObject({ status: 'success', metadata: { reasoning_degraded: 'true', outcome_summary: 'Turn limit reached' } })
  })
})
