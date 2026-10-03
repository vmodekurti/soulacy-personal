// @vitest-environment jsdom
//
// The feed has to show what the gateway actually returns: a finished run's
// output as a card under the agent's name, an approval as a "needs you" card
// with Approve/Deny, one canonical scheduled result, and a double-tap that saves.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { get } from 'svelte/store'
import { genieAsk } from '../lib/stores.js'

let cmp = null, target = null
const tick = (ms = 40) => new Promise(r => setTimeout(r, ms))

function stubGateway() {
  vi.stubGlobal('WebSocket', class { constructor() { setTimeout(() => this.onopen && this.onopen(), 0) } close() {} })
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const u = String(url && url.url ? url.url : url)
    let body = '{}'
    if (u.includes('/agents')) body = JSON.stringify({ agents: [{ id: 'genie', name: 'Genie', enabled: true }, { id: 'steward', name: 'Steward', enabled: true }] })
    else if (u.includes('/runs/ledger')) body = JSON.stringify({ runs: [
      { id: 'r1', agentId: 'genie', sessionId: 'sched-genie-1', status: 'success', ok: true, output: 'Here are the **best fares** for Chicago → Lisbon.', startedAt: '2026-09-21T03:19:00Z', updatedAt: '2026-09-21T03:19:52Z', steps: 4, durationMs: 5200, trigger: 'cron' },
      { id: 'r2', agentId: 'steward', status: 'pending', ok: true, output: '', startedAt: '2026-09-21T03:15:00Z' },
    ] })
    else if (u.includes('/mobile/deliveries')) body = JSON.stringify({ deliveries: [{ id: 'd1', agent_id: 'genie', session_id: 'sched-genie-1', title: 'Morning brief', body: 'Here are the **best fares** for Chicago → Lisbon.', created_at: '2026-09-21T11:30:00Z', metadata: { trigger: 'cron' } }] })
    else if (u.includes('/approvals')) body = JSON.stringify({ approvals: [{ call_id: 'c1', tool: 'shell_exec', args: { cmd: 'brew upgrade' }, reason: '3 security updates', agent_id: 'steward', created_at: '2026-09-21T12:00:00Z' }] })
    else if (u.includes('/activity/running')) body = JSON.stringify({ sessions: [{ agent_id: 'steward' }] })
    return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } })
  }))
}

async function mount() {
  const { default: Feed } = await import('./Feed.svelte')
  target = document.createElement('div'); document.body.appendChild(target)
  cmp = new Feed({ target }); await tick(60)
  return target.textContent.replace(/\s+/g, ' ')
}

describe('Feed', () => {
  beforeEach(() => { localStorage.clear(); stubGateway(); genieAsk.set(null) })
  afterEach(() => { if (cmp) cmp.$destroy(); if (target) target.remove(); cmp = null; target = null; genieAsk.set(null); vi.unstubAllGlobals() })

  it('shows one result for a run that was also delivered, and skips runs without output', async () => {
    const text = await mount()
    expect(text).toContain('Genie')
    expect(text).toContain('best fares')
    expect(target.querySelectorAll('article.card').length).toBe(2) // approval + one canonical result
    expect(text.match(/best fares/g)?.length).toBe(1)
  })

  it('puts an approval first as a needs-you card with Approve and Deny', async () => {
    await mount()
    const first = target.querySelector('article.card')
    expect(first.classList.contains('needs')).toBe(true)
    expect(first.textContent).toContain('shell_exec')
    expect([...first.querySelectorAll('button')].map(b => b.textContent)).toEqual(expect.arrayContaining(['Deny', 'Approve']))
  })

  it('offers a calm Home and a dedicated Results library', async () => {
    await mount()
    expect(target.querySelector('.stories')).toBeNull()
    expect(target.textContent).toContain('Agent results')
    const results = [...target.querySelectorAll('.scopes button')].find(b => b.textContent.includes('Results'))
    results.click(); await tick(20)
    expect(target.textContent).toContain('Scheduled work, organized by agent')
    expect(target.textContent).toContain('All agents')
  })

  it('hands the exact selected result to Genie', async () => {
    await mount()
    const card = [...target.querySelectorAll('article.card')].find(c => c.textContent.includes('Morning brief'))
    card.querySelector('.continue').click()
    const request = get(genieAsk)
    expect(request).toMatchObject({
      type: 'result',
      context: {
        resultId: 'result:sched-genie-1',
        sourceAgent: 'Genie',
        title: 'Morning brief',
      },
    })
    expect(request.context.body).toContain('best fares')
  })

  it('double-tap saves a card (heart on) and a second double-tap unsaves', async () => {
    await mount()
    const card = [...target.querySelectorAll('article.card')].find(c => c.textContent.includes('Morning brief'))
    card.click(); card.click(); await tick(10)
    expect(card.querySelector('.heart').classList.contains('on')).toBe(true)
    await tick(400)
    card.click(); card.click(); await tick(10)
    expect(card.querySelector('.heart').classList.contains('on')).toBe(false)
  })
})

describe('Feed cards read like posts', () => {
  beforeEach(() => { localStorage.clear(); stubGateway() })
  afterEach(() => { if (cmp) cmp.$destroy(); if (target) target.remove(); cmp = null; target = null; vi.unstubAllGlobals() })

  it('shows a headline from the first sentence and keeps the caption short', async () => {
    await mount()
    const card = [...target.querySelectorAll('article.card')].find(c => c.textContent.includes('Morning brief'))
    expect(card.querySelector('.title').textContent).toContain('Morning brief')
    expect(card.querySelector('.preview').textContent).toContain('Here are the')
  })
})
