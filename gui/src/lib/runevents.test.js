// A run is a handful of steps, not a list of events. These tests pin the
// folding, because it is what stops the activity panel running off the bottom
// of the screen — the complaint that started this.
import { describe, it, expect } from 'vitest'
import {
  groupRunEvents, liveActivity, runSummary, eventTitle, eventDetail,
  humanTool, isRunEvent, isPinnedToBottom,
} from './runevents.js'

const ev = (type, payload = {}, timestamp = '2026-09-24T10:00:00Z') => ({ type, payload, timestamp, agent_id: 'genie' })

describe('groupRunEvents', () => {
  it('folds a tool call and its result into one step', () => {
    const steps = groupRunEvents([
      ev('tool.call', { id: 'c1', name: 'web_search', arguments: { q: 'rates' } }),
      ev('tool.result', { call_id: 'c1', name: 'web_search', content: 'ten results' }),
    ])
    expect(steps).toHaveLength(1)
    expect(steps[0]).toMatchObject({ kind: 'tool', name: 'web_search', done: true, failed: false })
    expect(steps[0].title).toContain('web search')
  })

  it('pairs by call id even when results arrive out of order', () => {
    const steps = groupRunEvents([
      ev('tool.call', { id: 'a', name: 'alpha' }),
      ev('tool.call', { id: 'b', name: 'beta' }),
      ev('tool.result', { call_id: 'b', name: 'beta', content: 'B' }),
      ev('tool.result', { call_id: 'a', name: 'alpha', content: 'A' }),
    ])
    expect(steps).toHaveLength(2)
    expect(steps.every((s) => s.done)).toBe(true)
    expect(steps[0].name).toBe('alpha')
  })

  it('marks a failed tool so it can be seen without opening anything', () => {
    const steps = groupRunEvents([
      ev('tool.call', { id: 'c1', name: 'shell_exec' }),
      ev('tool.result', { call_id: 'c1', name: 'shell_exec', content: 'error: denied', is_error: true }),
    ])
    expect(steps[0].failed).toBe(true)
    expect(steps[0].title).toContain('failed')
  })

  it('leaves a call without a result pending, so it can pulse', () => {
    const steps = groupRunEvents([ev('tool.call', { id: 'c1', name: 'web_search' })])
    expect(steps[0].done).toBe(false)
  })

  it('folds a model turn into one step rather than a call and a result', () => {
    const steps = groupRunEvents([
      ev('llm.call', { model: 'glm-5.3', turn: 1 }),
      ev('llm.result', { output_tokens: 150, tool_calls: 1, duration_ms: 1586 }),
    ])
    expect(steps).toHaveLength(1)
    expect(steps[0].kind).toBe('model')
    expect(steps[0].done).toBe(true)
    expect(steps[0].title).toContain('1 tool')
  })

  it('ignores machinery that is not worth showing', () => {
    expect(groupRunEvents([ev('message.in'), ev('assistant.delta', { text: 'hi' })])).toHaveLength(0)
  })

  it('collapses the real install trace into something readable', () => {
    // Ten events from the run that prompted this work.
    const events = [
      ev('llm.call', { turn: 1 }), ev('llm.result', { tool_calls: 1 }),
      ev('tool.call', { id: '1', name: 'mcp_install_inspect' }), ev('tool.result', { call_id: '1', name: 'mcp_install_inspect', content: 'companion service' }),
      ev('llm.call', { turn: 2 }), ev('llm.result', { tool_calls: 1 }),
      ev('tool.call', { id: '2', name: 'package_install' }), ev('tool.result', { call_id: '2', name: 'package_install', content: 'error: installer failed', is_error: true }),
      ev('llm.call', { turn: 3 }), ev('llm.result', { tool_calls: 2 }),
    ]
    const steps = groupRunEvents(events)
    expect(steps).toHaveLength(5) // was ten rows
    expect(steps.filter((s) => s.failed)).toHaveLength(1)
    expect(runSummary(events)).toBe('2 tools · 3 turns · 1 failed')
  })
})

describe('liveActivity', () => {
  it('says what is happening right now, not what happened', () => {
    expect(liveActivity([])).toMatch(/Starting/)
    expect(liveActivity([ev('tool.call', { name: 'web_search' })])).toBe('Using web search…')
    expect(liveActivity([ev('llm.call', { turn: 2 })])).toBe('Thinking · turn 2…')
    expect(liveActivity([ev('llm.result', { tool_calls: 0 })])).toBe('Writing the answer…')
    expect(liveActivity([ev('error', { stage: 'llm' })])).toMatch(/problem/)
  })
})

describe('naming', () => {
  it('speaks of tools the way a person would', () => {
    expect(humanTool('web_search')).toBe('web search')
    expect(humanTool('mcp__github__create_issue')).toBe('create issue')
    expect(humanTool()).toBe('a tool')
  })

  it('keeps a detail to one line', () => {
    const long = eventDetail(ev('tool.result', { content: 'x'.repeat(500) }))
    expect(long.length).toBeLessThan(250)
    expect(long.endsWith('…')).toBe(true)
  })

  it('titles an error with its stage', () => {
    expect(eventTitle(ev('error', { stage: 'llm' }))).toBe('Problem in llm')
  })

  it('recognises only the events worth showing', () => {
    expect(isRunEvent(ev('tool.call'))).toBe(true)
    expect(isRunEvent(ev('assistant.delta'))).toBe(false)
    expect(isRunEvent(null)).toBe(false)
  })
})

describe('isPinnedToBottom', () => {
  // The rule that stops the panel stealing the scroll.
  const el = (scrollHeight, scrollTop, clientHeight) => ({ scrollHeight, scrollTop, clientHeight })

  it('is true at the bottom and within a little slack', () => {
    expect(isPinnedToBottom(el(1000, 800, 200))).toBe(true)
    expect(isPinnedToBottom(el(1000, 770, 200))).toBe(true)
  })

  it('is false once the reader has scrolled up to read something', () => {
    expect(isPinnedToBottom(el(1000, 300, 200))).toBe(false)
  })

  it('treats a missing element as pinned, so a first render still follows', () => {
    expect(isPinnedToBottom(null)).toBe(true)
  })
})
