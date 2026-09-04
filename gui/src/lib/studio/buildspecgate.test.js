import { describe, it, expect } from 'vitest'
import { unresolvedBlockers, specRows } from './buildspecview.js'

const specWithBlockers = {
  intent: 'do a thing',
  questions: [
    { id: 'delivery', question: 'Where should this go?', blocker: true },
    { id: 'sources', question: 'Which sources?', blocker: false },
  ],
}

describe('unresolvedBlockers', () => {
  it('reports a blocker that has not been answered', () => {
    expect(unresolvedBlockers(specWithBlockers, {}).map((b) => b.id)).toEqual(['delivery'])
  })

  it('clears once the user supplies a value', () => {
    expect(unresolvedBlockers(specWithBlockers, { delivery: '@vasu' })).toHaveLength(0)
  })

  it('does not count whitespace as an answer', () => {
    expect(unresolvedBlockers(specWithBlockers, { delivery: '   ' })).toHaveLength(1)
  })

  it('ignores non-blocking questions', () => {
    expect(unresolvedBlockers(specWithBlockers, {}).some((b) => b.id === 'sources')).toBe(false)
  })

  it('is safe on a missing spec or answers', () => {
    expect(unresolvedBlockers(null, null)).toEqual([])
    expect(unresolvedBlockers(undefined, {})).toEqual([])
  })

  // Both Generate buttons call this. The header one used to have its own
  // (absent) gate and generated straight past the required answers.
  it('gives the same verdict regardless of which button asks', () => {
    const a = unresolvedBlockers(specWithBlockers, {})
    const b = unresolvedBlockers(specWithBlockers, {})
    expect(a.map((x) => x.id)).toEqual(b.map((x) => x.id))
  })
})

describe('strategy row', () => {
  it('uses `reason` when the recommendation comes from the build-spec endpoint', () => {
    const rows = specRows({}, { mode: 'auto', reason: 'Interactive tool-capable agent.' })
    const strategy = rows.find((r) => r.key === 'strategy')
    expect(strategy.value).toContain('Auto')
    expect(strategy.detail).toBe('Interactive tool-capable agent.')
    expect(strategy.empty).toBe(false)
  })

  it('still uses `rationale` from a generated workflow', () => {
    const rows = specRows({}, { mode: 'workflow', rationale: 'Fixed pipeline.' })
    const strategy = rows.find((r) => r.key === 'strategy')
    expect(strategy.detail).toBe('Fixed pipeline.')
  })

  it('is empty only when there is genuinely no recommendation', () => {
    const strategy = specRows({}, null).find((r) => r.key === 'strategy')
    expect(strategy.empty).toBe(true)
  })
})
