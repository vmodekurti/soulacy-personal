import { describe, it, expect } from 'vitest'
import { responseNotice, executionLimitRows } from './runStatus.js'
describe('run status presentation', () => {
  it('keeps partial work distinct from verified completion', () => {
    expect(responseNotice({ reasoning_degraded: 'true', outcome_summary: 'Budget exhausted' })).toMatchObject({ title: 'Work is incomplete', detail: 'Budget exhausted' })
    expect(responseNotice({ outcome: 'unverified' }).tone).toBe('neutral')
    expect(responseNotice({ outcome: 'complete' })).toBeNull()
    expect(responseNotice(null)).toBeNull()
  })
  it('distinguishes unlimited from unavailable policy information', () => {
    expect(executionLimitRows(null)).toEqual([])
    expect(executionLimitRows({ max_turns: 0, max_tokens: 0, max_llm_calls: 0, run_timeout: 'unlimited' }).every(row => row[1] === 'Unlimited')).toBe(true)
    expect(executionLimitRows({ max_turns: -1 })).toEqual([])
  })
})
