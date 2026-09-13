import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const website = readFileSync(new URL('../../../website/index.html', import.meta.url), 'utf8')
const footprint = readFileSync(new URL('../../../docs/deployment/footprint.md', import.meta.url), 'utf8')

describe('public website claims', () => {
  it('distinguishes self-hosted Personal from Commercial SaaS', () => {
    expect(website).toContain('Self-host with Soulacy Personal, or use Soulacy Commercial as SaaS.')
    expect(website).toContain('https://docs.soulacy.io/editions/')
    expect(website).not.toContain('Not a hosted SaaS')
    expect(website).not.toContain('soulacy.cloud')
  })
  it('does not promise unmeasured universal resource limits or zero dependencies', () => {
    for (const claim of ['~25 MB', '&lt; 30 MB', '&lt; 50 ms', 'Instant boot', 'Zero external dependencies', 'dependency-free']) {
      expect(website).not.toContain(claim)
    }
    expect(website).toContain('44.2 MB measured example')
    expect(footprint).toContain('44,240,496 bytes')
    expect(footprint).toContain('candidate-build observation')
  })
  it('links all six walkthroughs and the footprint evidence', () => {
    for (const slug of ['notes-to-action-plan', 'handbook-answers', 'morning-brief', 'teach-a-preference', 'safe-undo-handoff', 'verified-release']) {
      expect(website).toContain(`https://docs.soulacy.io/use-cases/${slug}/`)
      expect(readFileSync(new URL(`../../../docs/use-cases/${slug}.md`, import.meta.url), 'utf8')).toMatch(/^# /)
    }
    expect(website).toContain('https://docs.soulacy.io/deployment/footprint/')
  })
})
