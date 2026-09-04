import { describe, it, expect } from 'vitest'
import {
  classifyFailure, failureSignature, groupFailures, categoryCounts, isRetryable,
  CAT_GRAPH, CAT_CONTRACT, CAT_CONFIG, CAT_PROVIDER, CAT_PERMISSION,
  CAT_DELIVERY, CAT_TRANSIENT, CAT_UNKNOWN,
} from './failuregroup.js'

describe('classifyFailure', () => {
  it('prefers an explicit server category over everything else', () => {
    expect(classifyFailure({ category: 'delivery', class: 'auth', error: 'timeout' })).toBe(CAT_DELIVERY)
  })

  it('maps the backend repair classes onto the story vocabulary', () => {
    expect(classifyFailure({ class: 'auth' })).toBe(CAT_PROVIDER)
    expect(classifyFailure({ class: 'permission' })).toBe(CAT_PERMISSION)
    expect(classifyFailure({ class: 'rate_limit' })).toBe(CAT_TRANSIENT)
    expect(classifyFailure({ class: 'shape_drift' })).toBe(CAT_CONTRACT)
    expect(classifyFailure({ class: 'template_error' })).toBe(CAT_GRAPH)
  })

  it('reads the repair class from either field name', () => {
    expect(classifyFailure({ repair_class: 'network' })).toBe(CAT_TRANSIENT)
  })

  it('falls back to prose only as a last resort', () => {
    expect(classifyFailure({ error: 'context deadline exceeded' })).toBe(CAT_TRANSIENT)
    expect(classifyFailure({ root_cause: 'Missing required input for studio_create' })).toBe(CAT_CONTRACT)
    expect(classifyFailure({ error: 'telegram recipient not configured' })).toBe(CAT_DELIVERY)
    expect(classifyFailure({ error: 'no consent granted for reading full article text' })).toBe(CAT_PERMISSION)
    expect(classifyFailure({ error: 'edge references unknown node "foo"' })).toBe(CAT_GRAPH)
  })

  it('returns unknown rather than guessing when there is nothing to go on', () => {
    // A misleading label sends someone troubleshooting in the wrong direction,
    // which is worse than admitting we do not know.
    expect(classifyFailure({})).toBe(CAT_UNKNOWN)
    expect(classifyFailure(null)).toBe(CAT_UNKNOWN)
    expect(classifyFailure({ error: 'something went sideways' })).toBe(CAT_UNKNOWN)
  })

  it('ignores an unrecognised explicit category rather than trusting it blindly', () => {
    expect(classifyFailure({ category: 'banana', class: 'auth' })).toBe(CAT_PROVIDER)
  })
})

describe('isRetryable', () => {
  it('is true only for transient faults', () => {
    expect(isRetryable(CAT_TRANSIENT)).toBe(true)
    expect(isRetryable(CAT_CONFIG)).toBe(false)
    expect(isRetryable(CAT_CONTRACT)).toBe(false)
  })
})

describe('failureSignature', () => {
  it('treats the same fault at the same node as one signature', () => {
    const a = { failed_node: 'studio_create', error: 'missing required input: source_pack.text' }
    const b = { failed_node: 'studio_create', error: 'missing required input: source_pack.text' }
    expect(failureSignature(a)).toBe(failureSignature(b))
  })

  it('separates the same message at different nodes', () => {
    const a = { failed_node: 'a', error: 'boom' }
    const b = { failed_node: 'b', error: 'boom' }
    expect(failureSignature(a)).not.toBe(failureSignature(b))
  })

  it('normalises ids, timestamps and numbers so per-run noise does not split a group', () => {
    const a = { failed_node: 'n', error: 'run 3f8a2b9c1d4e failed after 1200ms at 2026-07-24T10:00:00Z' }
    const b = { failed_node: 'n', error: 'run 9c2b1a8f4d3e failed after 1500ms at 2026-07-25T11:30:00Z' }
    expect(failureSignature(a)).toBe(failureSignature(b))
  })

  it('does NOT treat failedAt as an identifier — it is a timestamp', () => {
    // /studio/failed-runs sends `failedAt` as a time. Reading it as the subject
    // would give every run a unique signature and defeat grouping entirely.
    const a = { agentId: 'podcast', error: 'boom', failedAt: '2026-07-24T07:00:00Z' }
    const b = { agentId: 'podcast', error: 'boom', failedAt: '2026-07-25T07:00:00Z' }
    expect(failureSignature(a)).toBe(failureSignature(b))
  })

  it('falls back to the agent when no failing node is named', () => {
    const a = { agentId: 'podcast', error: 'boom' }
    const b = { agentId: 'digest', error: 'boom' }
    expect(failureSignature(a)).not.toBe(failureSignature(b))
  })
})

describe('groupFailures', () => {
  // The real /studio/failed-runs shape: agentId, error, attempts, failedAt.
  const runs = [
    { id: 'r1', agentId: 'podcast', error: 'missing required input', attempts: 1, failedAt: '2026-07-20T07:00:00Z' },
    { id: 'r2', agentId: 'podcast', error: 'missing required input', attempts: 1, failedAt: '2026-07-24T07:00:00Z' },
    { id: 'r3', agentId: 'podcast', error: 'missing required input', attempts: 1, failedAt: '2026-07-22T07:00:00Z' },
    { id: 'r4', agentId: 'podcast', error: 'telegram recipient not configured', attempts: 1, failedAt: '2026-07-21T07:00:00Z' },
  ]

  it('collapses repeats into one row with a count', () => {
    const g = groupFailures(runs)
    expect(g).toHaveLength(2)
    expect(g.find((x) => x.message.startsWith('missing')).count).toBe(3)
  })

  it('counts DLQ retries as occurrences, not one per queue entry', () => {
    // An entry that already retried five times is five failures, and reporting
    // it as one understates the problem.
    const g = groupFailures([
      { id: 'a', agentId: 'x', error: 'boom', attempts: 5, failedAt: '2026-07-24T07:00:00Z' },
      { id: 'b', agentId: 'x', error: 'boom', attempts: 3, failedAt: '2026-07-25T07:00:00Z' },
    ])
    expect(g[0].count).toBe(8)
    expect(g[0].entries).toBe(2)
  })

  it('sorts most-recent-first so what just broke is on top', () => {
    // Not by count: the loudest failure is rarely the newest information.
    expect(groupFailures(runs)[0].last).toBe(Date.parse('2026-07-24T07:00:00Z'))
  })

  it('records the first and last occurrence of a group', () => {
    const g = groupFailures(runs).find((x) => x.message.startsWith('missing'))
    expect(g.first).toBe(Date.parse('2026-07-20T07:00:00Z'))
    expect(g.last).toBe(Date.parse('2026-07-24T07:00:00Z'))
  })

  it('keeps the most recent run as the group representative', () => {
    const g = groupFailures(runs).find((x) => x.message.startsWith('missing'))
    expect(g.latest.id).toBe('r2')
    expect(g.runs).toHaveLength(3)
  })

  it('classifies each group', () => {
    const g = groupFailures(runs)
    expect(g.find((x) => x.message.includes('telegram')).category).toBe(CAT_DELIVERY)
  })

  it('handles missing timestamps and null payloads without throwing', () => {
    expect(groupFailures(null)).toEqual([])
    expect(groupFailures([null, { agentId: 'n', error: 'x' }])).toHaveLength(1)
  })
})

describe('categoryCounts', () => {
  it('totals occurrences per category, not groups', () => {
    const groups = [
      { category: CAT_CONTRACT, count: 3 },
      { category: CAT_DELIVERY, count: 1 },
      { category: CAT_CONTRACT, count: 2 },
    ]
    expect(categoryCounts(groups)).toEqual({ [CAT_CONTRACT]: 5, [CAT_DELIVERY]: 1 })
  })

  it('returns an empty object for no groups', () => {
    expect(categoryCounts([])).toEqual({})
    expect(categoryCounts(null)).toEqual({})
  })
})
