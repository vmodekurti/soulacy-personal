import { describe, it, expect } from 'vitest'
import { snapshotSession, restoreLandingStep, sessionAfterDelete, promptsForDraft } from './session.js'
import { STEP_DESCRIBE, STEP_BUILD } from './wizard.js'

const draft = { name: 'Weather', flow: { nodes: [{ id: 'a' }] } }

describe('snapshotSession', () => {
  it('carries work in progress across a navigation', () => {
    const s = snapshotSession({ workflow: draft, intent: 'weather agent', loadedAgentId: 'a1' })
    expect(s).not.toBeNull()
    expect(s.workflow).toBe(draft)
    expect(s.intent).toBe('weather agent')
  })

  // The reported case: save, go to Deployed, click Studio — and land back inside
  // the agent with no route to the home screen.
  it('carries nothing once the draft has been saved', () => {
    expect(snapshotSession({ workflow: draft, committed: true })).toBeNull()
  })

  it('carries nothing when the canvas is empty', () => {
    expect(snapshotSession({ workflow: null, intent: 'typed but not generated' })).toBeNull()
  })

  // Without the id, a restored canvas has no identity: the delete cannot match
  // it, and a save would create a duplicate rather than update.
  it('keeps the agent id with the draft', () => {
    expect(snapshotSession({ workflow: draft, loadedAgentId: 'agent-7' }).loadedAgentId).toBe('agent-7')
  })

  it('is safe on missing input', () => {
    expect(snapshotSession(null)).toBeNull()
    expect(snapshotSession({})).toBeNull()
  })

  it('defaults every field so a restore cannot read undefined', () => {
    const s = snapshotSession({ workflow: draft })
    expect(s.notes).toEqual([])
    expect(s.questions).toEqual([])
    expect(s.refineAnswers).toEqual({})
    expect(s.loadedAgentId).toBe('')
  })
})

describe('restoreLandingStep', () => {
  it('lands on the first step, not a computed resume step', () => {
    expect(restoreLandingStep(STEP_DESCRIBE)).toBe(STEP_DESCRIBE)
    expect(restoreLandingStep(STEP_DESCRIBE)).not.toBe(STEP_BUILD)
  })
})

describe('sessionAfterDelete', () => {
  it('drops a snapshot showing the deleted agent', () => {
    expect(sessionAfterDelete({ workflow: draft, loadedAgentId: 'a1' }, 'a1')).toBeNull()
  })

  it('keeps a snapshot of a different agent', () => {
    const other = { workflow: draft, loadedAgentId: 'a2' }
    expect(sessionAfterDelete(other, 'a1')).toBe(other)
  })

  // An unsaved draft has no id and cannot be the agent being deleted, so it must
  // survive — deleting some other agent must not discard the user's work.
  it('keeps an unsaved draft', () => {
    const unsaved = { workflow: draft, loadedAgentId: '' }
    expect(sessionAfterDelete(unsaved, 'a1')).toBe(unsaved)
  })

  it('is safe on missing input', () => {
    expect(sessionAfterDelete(null, 'a1')).toBeNull()
    expect(sessionAfterDelete({ loadedAgentId: 'a1' }, '')).toEqual({ loadedAgentId: 'a1' })
  })
})

describe('promptsForDraft', () => {
  it('adopts the draft its own prompt', () => {
    expect(promptsForDraft({ intent: 'summarize HN', raw_intent: 'hn digest' }))
      .toEqual({ intent: 'summarize HN', rawPrompt: 'hn digest' })
  })

  it('CLEARS both boxes for a draft with no stored prompt', () => {
    // The bug: a template/import inherited the previously-open agent's prompt,
    // so the box described something no longer on the canvas — and Generate
    // would have rebuilt from it.
    expect(promptsForDraft({ name: 'Weekday HN Digest', flow: { nodes: [] } }))
      .toEqual({ intent: '', rawPrompt: '' })
  })

  it('clears for a null / cleared canvas', () => {
    expect(promptsForDraft(null)).toEqual({ intent: '', rawPrompt: '' })
  })

  it('ignores non-string prompt fields rather than rendering [object Object]', () => {
    expect(promptsForDraft({ intent: { a: 1 }, raw_intent: 42 }))
      .toEqual({ intent: '', rawPrompt: '' })
  })
})
