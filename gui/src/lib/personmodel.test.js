import { describe, it, expect } from 'vitest'
import { provenance, isGuess, filledSections, fillProgress, nextStep } from './personmodel.js'

describe('provenance', () => {
  it('credits the person when they said it', () => {
    expect(provenance({ source: 'manual' })).toMatchObject({ label: 'You told us', tone: 'you' })
  })

  it('credits the person when an agent quoted them, and shows the quote', () => {
    const p = provenance({ source: 'agent:getting-to-know-you', value: { said: 'I live in Oak Park' } })
    expect(p.tone).toBe('you')
    expect(p.label).toBe('You told us')
    expect(p.detail).toContain('I live in Oak Park')
  })

  it('names the agent when it guessed', () => {
    expect(provenance({ source: 'agent:planner' })).toMatchObject({ label: 'Guessed by planner', tone: 'agent' })
  })

  it('names the sense when a device noticed it', () => {
    expect(provenance({ source: 'sense:routine' })).toMatchObject({ label: 'Noticed by routine', tone: 'sense' })
  })
})

describe('isGuess', () => {
  it('is never true for something you said, however it was recorded', () => {
    expect(isGuess({ source: 'manual', confidence: 0.1 })).toBe(false)
    expect(isGuess({ source: 'agent:x', confidence: 0.2, value: { said: 'yes' } })).toBe(false)
  })
  it('is true for a low-confidence inference', () => {
    expect(isGuess({ source: 'sense:routine', confidence: 0.5 })).toBe(true)
  })
  it('is false for a confident inference', () => {
    expect(isGuess({ source: 'sense:routine', confidence: 0.9 })).toBe(false)
  })
})

describe('filledSections', () => {
  it('keeps the reading order and drops empty sections', () => {
    const out = filledSections({ preferences: [{ key: 'a' }], identity: [{ key: 'b' }], routine: [] })
    expect(out.map((s) => s.id)).toEqual(['identity', 'preferences'])
    expect(out[0].title).toBe('Who you are')
    expect(out[0].blurb).toBeTruthy()
  })
})

describe('fillProgress', () => {
  it('counts filled sections and entries', () => {
    const p = fillProgress({ identity: [{}, {}], routine: [{}] })
    expect(p).toMatchObject({ filled: 2, total: 6, entries: 3 })
    expect(p.percent).toBe(33)
  })
  it('is zero when nothing is known', () => {
    expect(fillProgress({})).toMatchObject({ filled: 0, entries: 0, percent: 0 })
  })
})

describe('nextStep', () => {
  it('points a brand-new person at the agent rather than at settings', () => {
    expect(nextStep({}, [])).toContain('Getting to Know You')
  })
  it('suggests a sense when the routine is blank and none are on', () => {
    expect(nextStep({ identity: [{}] }, [{ sense: 'routine', enabled: false }])).toContain('Switch on a sense')
  })
  it('just lists the gaps once a sense is already on', () => {
    const step = nextStep({ identity: [{}] }, [{ sense: 'routine', enabled: true }])
    expect(step).toContain('Still blank')
    expect(step).toContain('your usual day')
  })
  it('says nothing when every section has something', () => {
    const full = Object.fromEntries(['identity', 'routine', 'state', 'relationships', 'commitments', 'preferences'].map((id) => [id, [{}]]))
    expect(nextStep(full, [])).toBe('')
  })
})
