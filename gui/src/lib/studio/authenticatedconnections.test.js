import { describe, expect, it } from 'vitest'
import { attachAuthenticatedSourceGuidance, connectionMatchesPrompt, suggestedConnectionIDs } from './authenticatedconnections.js'

const hbr = {
  id: 'conn-hbr', name: 'HBR Subscription', status: 'ready',
  base_url: 'https://hbr.org/login', allowed_domains: ['hbr.org'],
}

describe('authenticated connection matching', () => {
  it('matches a named subscription source and ignores unrelated prompts', () => {
    expect(connectionMatchesPrompt(hbr, 'Curate AI articles from my signed-in HBR subscription')).toBe(true)
    expect(connectionMatchesPrompt(hbr, 'Show the weather in Chicago')).toBe(false)
  })

  it('does not select disconnected or unauthorized connections', () => {
    expect(suggestedConnectionIDs([hbr, { ...hbr, id: 'stale', status: 'needs_auth' }], 'Read HBR', c => c.id !== 'conn-hbr')).toEqual([])
  })

  it('adds deterministic authenticated-source guidance once', () => {
    const first = attachAuthenticatedSourceGuidance('Do the research.', [hbr], ['conn-hbr'])
    const second = attachAuthenticatedSourceGuidance(first, [hbr], ['conn-hbr'])
    expect(first).toContain('use authenticated_fetch')
    expect(second.match(/## Authenticated Sources/g)).toHaveLength(1)
  })
})
