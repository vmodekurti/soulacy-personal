import { describe, it, expect } from 'vitest'
import { laneOf, workLaneOf, toLanes, triggerLabel, planReview, migrateEndpoints, LANES } from './planlanes.js'

const wf = {
  name: 'Stock report',
  trigger: { type: 'schedule', config: { cron: '0 9 * * *' } },
  channels: ['telegram'],
  flow: {
    entry: 'screen',
    nodes: [
      { id: 'screen', kind: 'tool', tool: 'run_stock_screener', description: 'Run Stock Screener' },
      { id: 'rank', kind: 'python', description: 'Rank Stocks', code: 'def run(inputs):\n    return sorted(inputs)' },
      { id: 'send', kind: 'tool', tool: 'send_telegram_message', description: 'Send Telegram Report' },
    ],
    edges: [
      { from: 'screen', to: 'rank' },
      { from: 'rank', to: 'send' },
    ],
  },
}

describe('laneOf', () => {
  it('routes kinds to lanes', () => {
    expect(laneOf({ kind: 'trigger' })).toBe('trigger')
    expect(laneOf({ kind: 'exit' })).toBe('delivery')
    expect(laneOf({ kind: 'tool' })).toBe('work')
  })
})

describe('triggerLabel', () => {
  it('renders schedule/http/channel/manual', () => {
    expect(triggerLabel({ type: 'schedule', config: { cron: '0 9 * * *' } })).toMatch(/schedule.*0 9/)
    expect(triggerLabel({ type: 'http' })).toMatch(/HTTP/)
    expect(triggerLabel({ type: 'channel' })).toMatch(/message/)
    expect(triggerLabel({ type: 'manual' })).toMatch(/manually/)
  })
})

describe('workLaneOf', () => {
  it('classifies work nodes into gather/think/act/verify', () => {
    expect(workLaneOf({ kind: 'tool', tool: 'run_stock_screener' })).toBe('gather')
    expect(workLaneOf({ kind: 'tool', tool: 'get_weather' })).toBe('gather')
    expect(workLaneOf({ kind: 'tool', tool: 'fetch_url' })).toBe('gather')
    expect(workLaneOf({ kind: 'python', description: 'Rank Stocks' })).toBe('think')
    expect(workLaneOf({ kind: 'agent' })).toBe('think')
    expect(workLaneOf({ kind: 'tool', tool: 'send_telegram_message' })).toBe('act')
    expect(workLaneOf({ kind: 'tool', tool: 'create_report' })).toBe('act')
    expect(workLaneOf({ kind: 'python', description: 'Validate the totals' })).toBe('verify')
    expect(workLaneOf({ kind: 'tool', tool: 'check_schema' })).toBe('verify')
  })
})

describe('LANES', () => {
  it('is the six ordered lanes', () => {
    expect(LANES.map((l) => l.key)).toEqual(['trigger', 'gather', 'think', 'act', 'verify', 'deliver'])
  })
})

describe('toLanes', () => {
  it('splits work into the six lanes and orders each by edges', () => {
    const lanes = toLanes(wf)
    expect(lanes.trigger[0].label).toMatch(/schedule/)
    // screen → gather, rank → think, send → act
    expect(lanes.gather.map((c) => c.id)).toEqual(['screen'])
    expect(lanes.think.map((c) => c.id)).toEqual(['rank'])
    expect(lanes.act.map((c) => c.id)).toEqual(['send'])
    expect(lanes.verify).toEqual([])
    // telegram channel becomes an implied deliver card
    expect(lanes.deliver.some((c) => /telegram/i.test(c.label))).toBe(true)
  })
  it('falls back to a console deliver card when nothing is set', () => {
    const lanes = toLanes({ flow: { nodes: [{ id: 'a', kind: 'tool', tool: 't' }], edges: [], entry: 'a' } })
    expect(lanes.deliver[0].label).toMatch(/return the result/i)
  })
})

describe('planReview', () => {
  const r = planReview(wf)
  it('summarizes when/does/tools/code/delivery', () => {
    expect(r.when).toMatch(/schedule/)
    expect(r.does).toEqual(['Run Stock Screener', 'Rank Stocks', 'Send Telegram Report'])
    expect(r.usesTools).toContain('Run Stock Screener')
    expect(r.usesCode).toContain('Rank Stocks')
    expect(r.deliversTo.some((d) => /telegram/i.test(d))).toBe(true)
  })
  it('flags the external send as a risk', () => {
    expect(r.risks.join(' ')).toMatch(/Send Telegram Report/)
  })
  it('lists the permissions needed', () => {
    expect(r.permissions).toContain('Run custom code')
    expect(r.permissions.join(' ')).toMatch(/externally/)
  })
  it('reports ready when nothing needs attention', () => {
    expect(r.ready).toBe(true)
  })
  it('collects attention items for incomplete blocks', () => {
    const bad = { flow: { entry: 'a', nodes: [{ id: 'a', kind: 'tool' }], edges: [] } }
    const rr = planReview(bad)
    expect(rr.ready).toBe(false)
    expect(rr.attention[0].label).toBeTruthy()
  })
})

describe('migrateEndpoints', () => {
  it('converts legacy trigger/exit nodes into settings and removes them', () => {
    const legacy = {
      channels: [],
      flow: {
        entry: 't',
        nodes: [
          { id: 't', kind: 'trigger', params: { kind: 'cron', config: { cron: '0 9 * * *' } } },
          { id: 'work', kind: 'tool', tool: 'run' },
          { id: 'x', kind: 'exit', params: { route: 'channel', config: { channel: 'telegram' } } },
        ],
        edges: [{ from: 't', to: 'work' }, { from: 'work', to: 'x' }],
      },
    }
    const m = migrateEndpoints(legacy)
    expect(m.flow.nodes.map((n) => n.id)).toEqual(['work'])   // structural nodes removed
    expect(m.flow.entry).toBe('work')                          // entry rewired to what trigger fed
    expect(m.trigger.type).toBe('schedule')
    expect(m.channels).toContain('telegram')
  })
  it('is a no-op when there are no structural nodes', () => {
    const wf2 = { flow: { entry: 'a', nodes: [{ id: 'a', kind: 'tool', tool: 't' }], edges: [] } }
    expect(migrateEndpoints(wf2)).toBe(wf2)
  })
})

describe('migrateEndpoints keeps configured non-channel exits', () => {
  const wf = (params) => ({
    trigger: { type: 'schedule' },
    channels: [],
    flow: {
      entry: 'step',
      nodes: [
        { id: 'step', kind: 'python' },
        { id: 'exit_1', kind: 'exit', params },
      ],
      edges: [{ from: 'step', to: 'exit_1' }],
    },
  })

  it('keeps an http exit and its config in the graph', () => {
    const m = migrateEndpoints(wf({ route: 'http', config: { url: 'https://example.test/hook' } }))
    const ex = m.flow.nodes.find((n) => n.id === 'exit_1')
    expect(ex).toBeTruthy()
    expect(ex.params.config.url).toBe('https://example.test/hook')
    expect(m.flow.edges).toHaveLength(1)
  })

  it('keeps a console exit in the graph', () => {
    const m = migrateEndpoints(wf({ route: 'console', config: {} }))
    expect(m.flow.nodes.find((n) => n.id === 'exit_1')).toBeTruthy()
  })

  it('still migrates a channel exit into workflow.channels and drops the node', () => {
    const m = migrateEndpoints(wf({ route: 'channel', config: { channel: 'telegram' } }))
    expect(m.channels).toContain('telegram')
    expect(m.flow.nodes.find((n) => n.id === 'exit_1')).toBeFalsy()
  })

  it('still migrates a legacy exit that names no route', () => {
    const m = migrateEndpoints(wf({}))
    expect(m.flow.nodes.find((n) => n.id === 'exit_1')).toBeFalsy()
  })
})
