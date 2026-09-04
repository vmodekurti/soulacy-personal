import { describe, it, expect } from 'vitest'
import { fixturesFromWorkflow, outcomeWithFixtures, hasFixtures } from './benchfixtures.js'

describe('fixturesFromWorkflow', () => {
  it('returns empty-but-usable state for a workflow that was never tested', () => {
    const f = fixturesFromWorkflow({ name: 'x' })
    expect(f.assertions).toEqual([])
    expect(f.mockText).toEqual({})
    expect(f.sampleInput).toBe('')
    expect(f.variables).toEqual({})
    expect(f.startNode).toBe('')
  })

  it('survives a null workflow', () => {
    expect(() => fixturesFromWorkflow(null)).not.toThrow()
    expect(fixturesFromWorkflow(null).assertions).toEqual([])
  })

  it('hydrates mocks as editable text, not objects', () => {
    const f = fixturesFromWorkflow({ outcome: { mocks: { search: { items: [1, 2] } } } })
    expect(typeof f.mockText.search).toBe('string')
    expect(JSON.parse(f.mockText.search)).toEqual({ items: [1, 2] })
  })

  it('fills assertion defaults so a partial saved row still renders', () => {
    const f = fixturesFromWorkflow({ outcome: { assertions: [{ target: 'a' }] } })
    expect(f.assertions[0]).toEqual({ target: 'a', op: 'contains', value: '' })
  })

  it('reads variables, environment, sample input and start node', () => {
    const f = fixturesFromWorkflow({
      outcome: {
        sample_input: 'hello',
        variables: { city: 'Austin' },
        environment: { STAGE: 'test' },
        start_node: 'summarize',
      },
    })
    expect(f.sampleInput).toBe('hello')
    expect(f.variables).toEqual({ city: 'Austin' })
    expect(f.environment).toEqual({ STAGE: 'test' })
    expect(f.startNode).toBe('summarize')
  })
})

describe('outcomeWithFixtures', () => {
  it('returns undefined when there is nothing to persist', () => {
    expect(outcomeWithFixtures(null, { assertions: [], mockText: {} })).toBeUndefined()
  })

  it('does not invent an outcome block from blank rows', () => {
    const out = outcomeWithFixtures(null, {
      assertions: [{ target: '', op: '', value: '' }],
      mockText: { a: '   ' },
      variables: { '': 'x' },
      sampleInput: '  ',
    })
    expect(out).toBeUndefined()
  })

  it('parses mock text back to JSON', () => {
    const out = outcomeWithFixtures(null, { mockText: { search: '{"items":[1]}' } })
    expect(out.mocks.search).toEqual({ items: [1] })
  })

  it('drops un-parseable mock text rather than persisting broken JSON', () => {
    const out = outcomeWithFixtures(null, {
      mockText: { good: '{"a":1}', broken: '{"a":' },
    })
    expect(out.mocks).toEqual({ good: { a: 1 } })
    expect(out.mocks.broken).toBeUndefined()
  })

  it('preserves goal and enforce from the existing outcome', () => {
    const out = outcomeWithFixtures(
      { goal: 'deliver a digest', enforce: 'warn' },
      { assertions: [{ target: 'result', op: 'exists' }] }
    )
    expect(out.goal).toBe('deliver a digest')
    expect(out.enforce).toBe('warn')
  })

  it('clears the operand for exists, which takes none', () => {
    const out = outcomeWithFixtures(null, {
      assertions: [{ target: 'result', op: 'exists', value: 'leftover' }],
    })
    expect(out.assertions[0].value).toBe('')
  })

  it('round-trips through hydration without drift', () => {
    const bench = {
      assertions: [{ target: 'result', op: 'contains', value: 'digest' }],
      mockText: { search: '{\n  "items": [\n    1\n  ]\n}' },
      sampleInput: 'today',
      variables: { city: 'Austin' },
      environment: { STAGE: 'test' },
      startNode: 'fmt',
    }
    const outcome = outcomeWithFixtures(null, bench)
    const back = fixturesFromWorkflow({ outcome })
    expect(back.assertions).toEqual(bench.assertions)
    expect(back.sampleInput).toBe('today')
    expect(back.variables).toEqual({ city: 'Austin' })
    expect(back.environment).toEqual({ STAGE: 'test' })
    expect(back.startNode).toBe('fmt')
    expect(JSON.parse(back.mockText.search)).toEqual({ items: [1] })
  })
})

describe('hasFixtures', () => {
  it('is false for an untested workflow', () => {
    expect(hasFixtures({ name: 'x' })).toBe(false)
    expect(hasFixtures(null)).toBe(false)
    expect(hasFixtures({ outcome: {} })).toBe(false)
  })

  it('is true when any fixture is present', () => {
    expect(hasFixtures({ outcome: { assertions: [{ target: 'a', op: 'exists' }] } })).toBe(true)
    expect(hasFixtures({ outcome: { mocks: { a: 1 } } })).toBe(true)
    expect(hasFixtures({ outcome: { sample_input: 'x' } })).toBe(true)
    expect(hasFixtures({ outcome: { start_node: 'n' } })).toBe(true)
  })

  it('ignores goal and enforce, which are not fixtures', () => {
    expect(hasFixtures({ outcome: { goal: 'g', enforce: 'warn' } })).toBe(false)
  })
})
