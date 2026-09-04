import { describe, it, expect } from 'vitest'
import { nodeIdsFromLines, problemNodeIds, computeRunState } from './runstate.js'

describe('nodeIdsFromLines', () => {
  it('extracts step/node ids from problem lines', () => {
    const ids = nodeIdsFromLines([
      'step "create_notebook": references {{ .missing }}',
      'node "b": tool arg invalid',
      'a line with no node ref',
    ])
    expect([...ids].sort()).toEqual(['b', 'create_notebook'])
  })
  it('is null-safe', () => {
    expect(nodeIdsFromLines(null).size).toBe(0)
    expect(nodeIdsFromLines([null, 123]).size).toBe(0)
  })
})

describe('problemNodeIds', () => {
  it('unions preflight blockers, dependency warnings, and residual lines', () => {
    const ids = problemNodeIds({
      report: { residual: ['step "post": runtime error'] },
      preflight: {
        blockers: [{ nodeId: 'create' }],
        warnings: [{ nodeId: 'gen', kind: 'dependency' }, { nodeId: 'x', kind: 'channel' }],
      },
    })
    expect([...ids].sort()).toEqual(['create', 'gen', 'post'])
  })
})

describe('computeRunState', () => {
  const nodes = [{ id: 'create' }, { id: 'gen' }, { id: 'post' }]
  const report = (extra) => ({ workflow: { flow: { nodes } }, ...extra })

  it('marks all nodes ok when the build verified and nothing is implicated', () => {
    const s = computeRunState({ report: report({ verified: true }), preflight: { ok: true } })
    expect(s).toEqual({ create: 'ok', gen: 'ok', post: 'ok' })
  })

  it('marks implicated nodes as problem even when others are ok', () => {
    const s = computeRunState({
      report: report({ ok: false, residual: ['step "post": failed'] }),
      preflight: { blockers: [{ nodeId: 'create' }] },
    })
    expect(s.create).toBe('problem')
    expect(s.post).toBe('problem')
    expect(s.gen).toBe('idle') // not green, not implicated, not repaired
  })

  it('marks repaired nodes when a changing attempt named them and build is not green', () => {
    const s = computeRunState({
      report: report({
        ok: false,
        attempts: [{ phase: 'repair', changed: true, problems: ['step "gen": dangling ref'] }],
      }),
      preflight: {},
    })
    expect(s.gen).toBe('repaired')
  })

  it('returns empty for no report', () => {
    expect(computeRunState({})).toEqual({})
  })
})
