import { describe, it, expect } from 'vitest'
import { computeFlowLayout, flowEdgeLabel, flowNodeLabel } from './flowgraph.js'

describe('computeFlowLayout', () => {
  const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'island' }]
  const edges = [
    { from: 'a', to: 'b' },
    { from: 'b', to: 'c' },
    { from: 'c', to: 'a', max_iterations: 3 }, // back edge must not break layout
  ]

  it('assigns BFS depth columns from the entry', () => {
    const l = computeFlowLayout(nodes, edges, 'a')
    expect(l.get('a').col).toBe(0)
    expect(l.get('b').col).toBe(1)
    expect(l.get('c').col).toBe(2)
  })

  it('puts unreachable nodes in a trailing column', () => {
    const l = computeFlowLayout(nodes, edges, 'a')
    expect(l.get('island').col).toBe(3)
  })

  it('defaults entry to the first node', () => {
    const l = computeFlowLayout(nodes, edges, undefined)
    expect(l.get('a').col).toBe(0)
  })

  it('stacks same-column nodes in rows', () => {
    const l = computeFlowLayout(
      [{ id: 'root' }, { id: 'x' }, { id: 'y' }],
      [{ from: 'root', to: 'x' }, { from: 'root', to: 'y' }],
      'root',
    )
    expect(l.get('x').col).toBe(1)
    expect(l.get('y').col).toBe(1)
    expect(l.get('x').row).not.toBe(l.get('y').row)
  })
})

describe('flowEdgeLabel', () => {
  it('renders predicate and cycle budget', () => {
    expect(flowEdgeLabel({ if: '{{not .ok}}', max_iterations: 3 })).toContain('↺×3')
    expect(flowEdgeLabel({ if: '{{not .ok}}', max_iterations: 3 })).toContain('{{not .ok}}')
  })
  it('empty for plain edges', () => {
    expect(flowEdgeLabel({})).toBe('')
  })
  it('truncates long predicates', () => {
    const label = flowEdgeLabel({ if: 'x'.repeat(60) })
    expect(label.length).toBeLessThan(35)
  })
})

describe('flowNodeLabel', () => {
  it('labels tool, agent, branch nodes', () => {
    expect(flowNodeLabel({ id: 'n', tool: 'search' })).toContain('search')
    expect(flowNodeLabel({ id: 'n', agent: 'peer' })).toContain('peer')
    expect(flowNodeLabel({ id: 'n' })).toContain('◇')
  })
})
