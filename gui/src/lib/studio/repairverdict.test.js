import { describe, it, expect } from 'vitest'
import { repairVerdict, repairTone, repairProofLabel } from './repairverdict.js'

const proposal = { node_id: 'summarize' }

describe('repairVerdict', () => {
  it('says a seeded, promoted repair was proven against the real failing run', () => {
    const res = {
      applied: true,
      attempt: { node_id: 'summarize', promoted: true, validated: true, replayed: true },
      verification: { replayed: true, passed: true, evidence_seeded: true },
    }
    const msg = repairVerdict(res, proposal)
    expect(msg).toContain('summarize')
    expect(msg).toContain('replayed the failing run')
    expect(repairTone(res)).toBe('ok')
    expect(repairProofLabel(res)).toBe('proven against the failing run')
  })

  it('does not claim the failing run was replayed when no evidence was seeded', () => {
    const res = {
      applied: true,
      attempt: { node_id: 'summarize', promoted: true, validated: true, replayed: true },
      verification: { replayed: true, passed: true, evidence_seeded: false },
    }
    const msg = repairVerdict(res, proposal)
    expect(msg).toContain('sandbox')
    expect(msg).not.toContain('failing run against it')
    expect(repairProofLabel(res)).toBe('proven in sandbox')
  })

  it('marks an applied-but-unproven repair as unproven and repeats the server note', () => {
    const res = {
      applied: true,
      attempt: { node_id: 'summarize', promoted: false, unproven: true, validated: true },
      verification: { replayed: false, evidence_seeded: false, note: 'no node_trace was supplied.' },
    }
    const msg = repairVerdict(res, proposal)
    expect(msg).toContain('Not yet proven')
    expect(msg).toContain('no node_trace was supplied.')
    // The critical distinction: applied, but must NOT read as verified.
    expect(msg).not.toContain('the fix holds')
    expect(repairTone(res)).toBe('warn')
    expect(repairProofLabel(res)).toBe('unproven')
  })

  it('explains a rollback instead of reporting a generic failure', () => {
    const res = {
      applied: false,
      rolled_back: true,
      attempt: {
        node_id: 'summarize', promoted: false, validated: true, replayed: true,
        reason: 'the repaired step ran but produced no output',
      },
      verification: { replayed: true, passed: false, evidence_seeded: true },
    }
    const msg = repairVerdict(res, proposal)
    expect(msg).toContain('Reverted')
    expect(msg).toContain('produced no output')
    expect(repairTone(res)).toBe('error')
    // Nothing was applied, so there is no proof to badge.
    expect(repairProofLabel(res)).toBe('')
  })

  it('reports a security-class refusal as a deliberate decision', () => {
    const res = {
      applied: false,
      attempt: { node_id: 'summarize', validated: false },
      failure: { class: 'auth', security: true, summary: 'credentials were rejected; fix the credential, not the workflow.' },
    }
    const msg = repairVerdict(res, proposal)
    expect(msg).toContain('Not repaired')
    expect(msg).toContain('fix the credential')
    expect(repairTone(res)).toBe('error')
  })

  it('surfaces a stale proposal that matched no node', () => {
    const res = {
      applied: false,
      attempt: { validated: false, reason: 'the proposal did not match any node in the draft' },
    }
    expect(repairVerdict(res, proposal)).toContain('did not match any node')
  })

  it('falls back to the proposal node id when the attempt record is thin', () => {
    const res = { applied: true, attempt: { promoted: true }, verification: { evidence_seeded: true } }
    expect(repairVerdict(res, proposal)).toContain('summarize')
  })

  it('returns empty for a missing response rather than inventing a verdict', () => {
    expect(repairVerdict(null, proposal)).toBe('')
    expect(repairTone(null)).toBe('')
    expect(repairProofLabel(null)).toBe('')
  })
})
