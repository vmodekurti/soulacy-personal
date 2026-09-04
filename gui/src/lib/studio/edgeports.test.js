import { describe, it, expect } from 'vitest'
import { toFlow } from './graph.js'

/*
 * Edge ports travel over the wire as from_port / to_port (SOUL.yaml and the Go
 * FlowEdge struct). The canvas used to read only the camelCase spelling, so a
 * port the server assigned never reached xyflow's sourceHandle/targetHandle,
 * and a port the Inspector wrote was dropped by the server on save.
 */
describe('graph.js edge ports', () => {
  const wf = (edge) => ({
    trigger: { type: 'manual' },
    channels: [],
    flow: {
      entry: 'a',
      nodes: [
        { id: 'a', kind: 'python', x: 0, y: 0, outputs: [{ name: 'ok' }, { name: 'err' }] },
        { id: 'b', kind: 'python', x: 200, y: 0, inputs: [{ name: 'in' }] },
      ],
      edges: [edge],
    },
  })
  const wire = (edge) => toFlow(wf(edge)).edges.find((x) => x.source === 'a' && x.target === 'b')

  it('maps wire-format from_port/to_port onto the canvas handles', () => {
    const e = wire({ from: 'a', to: 'b', from_port: 'err', to_port: 'in' })
    expect(e.sourceHandle).toBe('err')
    expect(e.targetHandle).toBe('in')
  })

  it('still honours legacy camelCase ports in older drafts', () => {
    const e = wire({ from: 'a', to: 'b', fromPort: 'ok', toPort: 'in' })
    expect(e.sourceHandle).toBe('ok')
    expect(e.targetHandle).toBe('in')
  })

  it('leaves handles undefined when no port is named', () => {
    const e = wire({ from: 'a', to: 'b' })
    expect(e.sourceHandle).toBeUndefined()
    expect(e.targetHandle).toBeUndefined()
  })
})
