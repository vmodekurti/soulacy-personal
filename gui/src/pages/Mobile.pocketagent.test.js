// @vitest-environment jsdom
//
// Pocket chat picks an agent once the agent list arrives.
//
// This is the behaviour that `$: ensurePocketAgent()` — the argument-less form —
// silently lost. With no visible dependency, Svelte emitted the call once in the
// instance body: it ran during init (throwing, because the reactive `chatAgents`
// declaration had not been assigned yet) and never ran again. Even with the
// throw guarded, the picker would have stayed empty and Send stayed disabled.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const AGENTS = {
  agents: [
    { id: 'cron-only', enabled: true, trigger: 'cron' },   // not chat-eligible
    { id: 'helper', enabled: true, trigger: 'chat' },
    { id: 'disabled-one', enabled: false, trigger: 'chat' },
  ],
  interfaces: {},
}

let cmp = null
let target = null

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const path = String(url)
    const body = path.endsWith('/agents') ? AGENTS : {}
    return new Response(JSON.stringify(body), {
      status: 200, headers: { 'content-type': 'application/json' },
    })
  }))
})

afterEach(() => {
  if (cmp) cmp.$destroy()
  if (target) target.remove()
  cmp = null
  target = null
  vi.unstubAllGlobals()
})

describe('Mobile · pocket chat', () => {
  it('selects the first chat-eligible agent when the list loads', async () => {
    const { default: Mobile } = await import('./Mobile.svelte')
    target = document.createElement('div')
    document.body.appendChild(target)
    cmp = new Mobile({ target })

    // Nothing to pick from yet, and — the original crash — getting this far at all.
    expect(target.textContent).toContain('No chat-ready agents are enabled.')

    await new Promise((r) => setTimeout(r, 50))   // let load() settle

    const select = target.querySelector('select[aria-label="Pocket chat agent"]')
    expect(select, 'the agent picker should appear once agents load').toBeTruthy()
    expect(select.value).toBe('helper')

    const send = [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Send')
    expect(send.disabled, 'Send stays disabled while no agent is selected').toBe(true)
  })
})
