// @vitest-environment jsdom
// The panel that shows what an agent is doing. Two properties matter more
// than the markup: it must be bounded (it used to grow the page until you
// could not reach anything), and it must not steal the scroll from a reader
// who has deliberately scrolled up to read an earlier step.
import { describe, it, expect, afterEach, vi } from 'vitest'

let cmp = null, target = null
afterEach(() => { if (cmp) cmp.$destroy(); if (target) target.remove(); cmp = null; target = null })

const ev = (type, payload = {}) => ({ type, payload, timestamp: '2026-09-24T10:00:00Z', agent_id: 'genie' })

async function mount(props) {
  const { default: RunActivity } = await import('./RunActivity.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  cmp = new RunActivity({ target, props })
  await new Promise((r) => setTimeout(r, 20))
  return target
}

const toolRun = [
  ev('llm.call', { turn: 1 }),
  ev('llm.result', { tool_calls: 1 }),
  ev('tool.call', { id: '1', name: 'list_skills' }),
  ev('tool.result', { call_id: '1', name: 'list_skills', content: 'eight skills' }),
]

describe('RunActivity', () => {
  it('shows the steps of a run, folded', async () => {
    const el = await mount({ events: toolRun, live: true })
    const steps = el.querySelectorAll('li.step')
    expect(steps).toHaveLength(2) // four events, two steps
    expect(el.textContent).toContain('list skills')
  })

  it('says what is happening right now while live', async () => {
    const el = await mount({ events: [ev('tool.call', { name: 'web_search' })], live: true })
    expect(el.querySelector('.now')?.textContent).toContain('Using web search')
  })

  it('keeps every step inside its own scroller, however many there are', async () => {
    // jsdom has no layout, so the pixel cap itself is verified in the built
    // stylesheet rather than here. What this pins is the structure that makes
    // the cap possible: one scroll container that owns all the steps, instead
    // of steps rendering straight into the page the way the old panel did.
    const many = Array.from({ length: 60 }, (_, i) => ev('tool.call', { id: `c${i}`, name: `tool_${i}` }))
    const el = await mount({ events: many, live: true })
    const body = el.querySelector('.body')
    expect(body).toBeTruthy()
    expect(el.querySelectorAll('li.step')).toHaveLength(60)
    for (const step of el.querySelectorAll('li.step')) {
      expect(body.contains(step)).toBe(true)
    }
    // Nothing may pull the cap off at runtime.
    expect(body.getAttribute('style') || '').not.toContain('max-height')
  })

  it('surfaces a failure in the collapsed header, so nobody has to go looking', async () => {
    const failed = [
      ev('tool.call', { id: '1', name: 'shell_exec' }),
      ev('tool.result', { call_id: '1', name: 'shell_exec', content: 'error: denied', is_error: true }),
    ]
    const el = await mount({ events: failed, live: false, open: false })
    expect(el.querySelector('.failed-badge')?.textContent).toContain('1 failed')
  })

  it('summarises the run when collapsed and finished', async () => {
    const el = await mount({ events: toolRun, live: false, open: false })
    expect(el.querySelector('.meta')?.textContent).toContain('1 tool')
    expect(el.querySelectorAll('li.step')).toHaveLength(0) // closed: nothing rendered
  })

  it('renders nothing at all for a run with no activity', async () => {
    const el = await mount({ events: [], live: false })
    expect(el.querySelector('section.run')).toBeNull()
  })

  it('opens a step to its full payload only on request', async () => {
    const el = await mount({ events: toolRun, live: true, open: true })
    expect(el.querySelector('pre.full')).toBeNull()
    const expandable = [...el.querySelectorAll('.step-title')].find((b) => !b.disabled)
    expandable.click()
    await new Promise((r) => setTimeout(r, 20))
    expect(el.querySelector('pre.full')).toBeTruthy()
  })
})
