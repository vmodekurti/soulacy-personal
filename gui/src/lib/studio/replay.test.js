import { describe, it, expect } from 'vitest'
import { buildReplayFrames } from './replay.js'

// A synthetic trace mirroring the real /studio/build-trace event shape: two
// attempts (node "b" is a problem, gets repaired) then a verified result.
const trace = {
  events: [
    { kind: 'snapshot', phase: 'attempt-start', attempt: 1, data: { node_ids: ['a', 'b'] } },
    { kind: 'preflight', attempt: 1, data: { problems: ['step "b": references {{ .missing }}'] } },
    { kind: 'repair', attempt: 1, data: { problems: ['step "b": references {{ .missing }}'], changed: true } },
    { kind: 'snapshot', phase: 'attempt-start', attempt: 2, data: { node_ids: ['a', 'b'] } },
    { kind: 'preflight', attempt: 2, data: { problems: [] } },
    { kind: 'verify', attempt: 2, data: { ok: true, real: true } },
    { kind: 'result', phase: 'done', data: { ok: true, verified: true } },
  ],
}

describe('buildReplayFrames', () => {
  it('produces a frame per attempt plus a final verdict frame', () => {
    const frames = buildReplayFrames(trace)
    expect(frames.length).toBe(3) // attempt 1, attempt 2, result
    expect(frames[0].label).toBe('Attempt 1')
    expect(frames[2].label).toMatch(/Verified/)
  })

  it('marks the implicated node as a problem on the attempt it is implicated', () => {
    const f = buildReplayFrames(trace)
    expect(f[0].byId.b).toBe('problem')
    expect(f[0].byId.a).toBe('idle')
  })

  it('shows the node repaired on the next attempt once it clears', () => {
    const f = buildReplayFrames(trace)
    expect(f[1].byId.b).toBe('repaired') // was a problem in attempt 1, gone in 2
  })

  it('the final frame is all-ok on a verified build', () => {
    const f = buildReplayFrames(trace)
    const final = f[f.length - 1]
    expect(final.byId.a).toBe('ok')
    expect(final.byId.b).toBe('ok')
  })

  it('keeps a still-unresolved node red at the result on a failed build', () => {
    const failed = {
      events: [
        { kind: 'snapshot', phase: 'attempt-start', attempt: 1, data: { node_ids: ['a', 'b'] } },
        { kind: 'preflight', attempt: 1, data: { problems: ['step "b": broken'] } },
        { kind: 'result', phase: 'done', data: { ok: false, verified: false } },
      ],
    }
    const f = buildReplayFrames(failed)
    const final = f[f.length - 1]
    expect(final.byId.b).toBe('problem')
    expect(final.label).toBe('Result')
  })

  it('returns [] when there is nothing to replay', () => {
    expect(buildReplayFrames({ events: [] })).toEqual([])
    expect(buildReplayFrames(null)).toEqual([])
  })
})
