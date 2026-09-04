import { describe, it, expect } from 'vitest'
import {
  STEPS, STEP_DESCRIBE, STEP_BUILD, STEP_TEST, STEP_SAVE,
  stepIndex, hasDraft, canEnter, isDone, stepStates,
  nextStep, prevStep, autoStep, saveBlockedReason, generatedMode,
} from './wizard.js'

const empty = {}
const described = { intent: 'daily podcast' }
const built = { intent: 'daily podcast', hasNodes: true }
const tested = { ...built, tested: true, testPassed: true }
const saved = { ...tested, saved: true }

describe('generatedMode', () => {
  it('never selects experimental Workflow during ordinary generation', () => {
    expect(generatedMode('workflow')).toBe('auto')
  })

  it('preserves supported agent strategies', () => {
    expect(generatedMode('auto')).toBe('auto')
    expect(generatedMode('react')).toBe('react')
    expect(generatedMode('plan_execute')).toBe('plan_execute')
  })

  it('defaults missing or malformed recommendations to Auto', () => {
    expect(generatedMode('')).toBe('auto')
    expect(generatedMode('something-new')).toBe('auto')
  })
})

describe('canEnter', () => {
  it('always allows Describe', () => {
    expect(canEnter(STEP_DESCRIBE, empty).ok).toBe(true)
  })

  it('blocks Build with nothing to build from, and says why', () => {
    const r = canEnter(STEP_BUILD, empty)
    expect(r.ok).toBe(false)
    expect(r.reason).toMatch(/Describe/)
  })

  it('allows Build from an intent alone, before any graph exists', () => {
    expect(canEnter(STEP_BUILD, described).ok).toBe(true)
  })

  it('allows Build for a BROKEN draft — that is when you need the canvas most', () => {
    expect(canEnter(STEP_BUILD, { hasNodes: true, readiness: { blockers: [{}, {}] } }).ok).toBe(true)
  })

  it('allows an agent draft with no nodes to reach Build and Test', () => {
    const agent = { isAgent: true }
    expect(canEnter(STEP_BUILD, agent).ok).toBe(true)
    expect(canEnter(STEP_TEST, agent).ok).toBe(true)
  })

  it('opens Save even when saving is impossible — the step is where the reasons live', () => {
    const blocked = { hasNodes: true, readiness: { blockers: [{ message: 'no destination' }] } }
    expect(canEnter(STEP_SAVE, blocked).ok).toBe(true)
    expect(saveBlockedReason(blocked)).toMatch(/1 blocker/)
  })

  it('rejects an unknown step id rather than defaulting to allowed', () => {
    expect(canEnter('nope', built).ok).toBe(false)
  })
})

describe('isDone', () => {
  it('marks Describe done once there is an intent', () => {
    expect(isDone(STEP_DESCRIBE, empty)).toBe(false)
    expect(isDone(STEP_DESCRIBE, { intent: '   ' })).toBe(false)
    expect(isDone(STEP_DESCRIBE, described)).toBe(true)
  })

  it('does NOT mark Test done just because a test ran', () => {
    // A red test is not progress; the rail must not imply it was.
    expect(isDone(STEP_TEST, { ...built, tested: true, testPassed: false })).toBe(false)
    expect(isDone(STEP_TEST, tested)).toBe(true)
  })

  it('marks Save done only after an actual save', () => {
    expect(isDone(STEP_SAVE, tested)).toBe(false)
    expect(isDone(STEP_SAVE, saved)).toBe(true)
  })
})

describe('stepStates', () => {
  it('marks the active step active even when its work is done', () => {
    const s = stepStates(STEP_DESCRIBE, built)
    expect(s[0].status).toBe('active')
    expect(s[1].status).toBe('done')
  })

  it('marks unreachable steps blocked and carries the reason', () => {
    const s = stepStates(STEP_DESCRIBE, empty)
    expect(s[1].status).toBe('blocked')
    expect(s[1].reason).toBeTruthy()
    expect(s[1].enterable).toBe(false)
  })

  it('numbers steps from 1 for display', () => {
    expect(stepStates(STEP_DESCRIBE, empty).map((s) => s.index)).toEqual([1, 2, 3, 4])
  })

  it('returns one entry per declared step', () => {
    expect(stepStates(STEP_BUILD, built)).toHaveLength(STEPS.length)
  })
})

describe('nextStep / prevStep', () => {
  it('skips steps that cannot be entered', () => {
    // From Describe with nothing built, every later step is blocked.
    expect(nextStep(STEP_DESCRIBE, empty)).toBe('')
  })

  it('advances to the next reachable step', () => {
    expect(nextStep(STEP_DESCRIBE, built)).toBe(STEP_BUILD)
    expect(nextStep(STEP_BUILD, built)).toBe(STEP_TEST)
  })

  it('returns empty at the end rather than wrapping', () => {
    expect(nextStep(STEP_SAVE, saved)).toBe('')
    expect(prevStep(STEP_DESCRIBE)).toBe('')
  })

  it('goes back one step', () => {
    expect(prevStep(STEP_TEST)).toBe(STEP_BUILD)
  })
})

describe('autoStep', () => {
  it('lands a fresh session on Describe', () => {
    expect(autoStep(STEP_DESCRIBE, empty)).toBe(STEP_DESCRIBE)
  })

  it('moves a freshly generated workflow forward to Test, not back to Describe', () => {
    expect(autoStep(STEP_DESCRIBE, built)).toBe(STEP_TEST)
  })

  it('never yanks the user backwards', () => {
    // Editing a saved workflow invalidates the test result; the user is on Save
    // and must not be dragged back to Test mid-review.
    const edited = { ...built, testPassed: false }
    expect(autoStep(STEP_SAVE, edited)).toBe(STEP_SAVE)
  })

  it('settles on Save when everything is done', () => {
    expect(autoStep(STEP_TEST, saved)).toBe(STEP_SAVE)
  })
})

describe('saveBlockedReason', () => {
  it('is empty when saving is allowed', () => {
    expect(saveBlockedReason({ hasNodes: true, readiness: { blockers: [], unknown: [] } })).toBe('')
  })

  it('reports blockers', () => {
    expect(saveBlockedReason({ hasNodes: true, readiness: { blockers: [{}, {}] } })).toMatch(/2 blockers/)
  })

  it('refuses to confirm safety when a check could not run', () => {
    // The whole point of the unified readiness endpoint: unknown is not pass.
    const r = saveBlockedReason({ hasNodes: true, readiness: { blockers: [], unknown: ['security'] } })
    expect(r).toMatch(/could not run/)
  })

  it('allows saving when readiness has not been fetched yet', () => {
    // Absent readiness means "not checked", and blocking on it would deadlock
    // the very first save of a brand-new draft.
    expect(saveBlockedReason({ hasNodes: true })).toBe('')
  })

  it('reports nothing to save for an empty draft', () => {
    expect(saveBlockedReason(empty)).toMatch(/nothing to save/)
  })
})

describe('stepIndex / hasDraft', () => {
  it('locates steps and rejects unknown ids', () => {
    expect(stepIndex(STEP_TEST)).toBe(2)
    expect(stepIndex('nope')).toBe(-1)
  })
  it('treats a node-bearing flow or an agent as a draft', () => {
    expect(hasDraft({ hasNodes: true })).toBe(true)
    expect(hasDraft({ isAgent: true })).toBe(true)
    expect(hasDraft({})).toBe(false)
    expect(hasDraft(null)).toBe(false)
  })
})
