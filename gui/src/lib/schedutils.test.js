import { describe, it, expect } from 'vitest'
import { catchupLabel, catchupTitle } from './schedutils.js'

describe('catchupLabel', () => {
  it('labels catch-up agents with their window', () => {
    expect(catchupLabel({ catch_up: true, catch_up_window: '6h' }))
      .toBe('catch up · 6h window')
  })
  it('defaults the window to 24h', () => {
    expect(catchupLabel({ catch_up: true })).toBe('catch up · 24h window')
  })
  it('labels non-catch-up agents as skip', () => {
    expect(catchupLabel({ catch_up: false })).toBe('skip')
    expect(catchupLabel({})).toBe('skip')
    expect(catchupLabel(null)).toBe('skip')
  })
})

describe('catchupTitle', () => {
  it('explains catch-up semantics: latest-only, once, persisted', () => {
    const t = catchupTitle({ catch_up: true, catch_up_window: '6h' })
    expect(t).toContain('LATEST missed fire')
    expect(t).toContain('6h')
    expect(t).toContain('restarts')
  })
  it('explains how to enable it when off', () => {
    expect(catchupTitle({})).toContain('run_missed_on_startup')
  })
})
