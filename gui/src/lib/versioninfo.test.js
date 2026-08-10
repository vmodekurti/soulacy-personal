import { describe, it, expect } from 'vitest'
import { versionSummary, lastCheckedLabel } from './versioninfo.js'

describe('what version am I running', () => {
  // The bug. Being up to date was the one state with nothing to show, so the
  // answer to "what am I on?" was only ever available when you were behind.
  it('reports the version when there is no update — the common case', () => {
    const s = versionSummary({ current_version: 'v0.1.5', latest_version: '0.1.5', update_available: false })
    expect(s.version).toBe('v0.1.5')
    expect(s.status).toBe('current')
  })

  it('still reports the current version when an update exists', () => {
    const s = versionSummary({ current_version: 'v0.1.3', latest_version: '0.1.5', update_available: true })
    expect(s.version).toBe('v0.1.3')
    expect(s.status).toBe('behind')
    expect(s.detail).toContain('0.1.5')
  })

  it('says a version is available even when the payload omits the number', () => {
    const s = versionSummary({ current_version: 'v0.1.3', update_available: true })
    expect(s.detail).toMatch(/newer version is available/i)
  })

  it('shows the version it already knows while a check is in flight', () => {
    const s = versionSummary({ current_version: 'v0.1.5', checking: true })
    expect(s.version).toBe('v0.1.5')
    expect(s.status).toBe('checking')
  })

  // An unreachable update service is not evidence of being current. Claiming
  // "Up to date." here would be a confident wrong answer, which is the failure
  // mode worth guarding — the user acts on it and stops looking.
  it('does not claim you are up to date when it could not ask', () => {
    const s = versionSummary(null)
    expect(s.status).toBe('unknown')
    expect(s.detail).not.toMatch(/up to date/i)
    expect(s.detail).toMatch(/--version/)
  })

  it('handles a payload with no version string rather than rendering blank', () => {
    expect(versionSummary({ update_available: false }).version).toBe('unknown')
    expect(versionSummary({ current_version: '   ' }).version).toBe('unknown')
  })
})

describe('when it last checked', () => {
  const now = new Date('2026-08-07T12:00:00Z')
  const at = (iso) => lastCheckedLabel(iso, now)

  it('reads as an age, not a timestamp', () => {
    expect(at('2026-08-07T11:58:00Z')).toBe('checked 2 minutes ago')
    expect(at('2026-08-07T11:00:00Z')).toBe('checked 1 hour ago')
    expect(at('2026-08-05T12:00:00Z')).toBe('checked 2 days ago')
  })

  it('says nothing rather than something wrong', () => {
    expect(at('')).toBe('')
    expect(at('not-a-date')).toBe('')
    expect(at(undefined)).toBe('')
    // A clock skew that puts the check in the future should not print
    // "checked -3 minutes ago".
    expect(at('2026-08-07T12:05:00Z')).toBe('')
  })

  it('gets the singular right', () => {
    expect(at('2026-08-07T11:59:00Z')).toBe('checked 1 minute ago')
  })
})
