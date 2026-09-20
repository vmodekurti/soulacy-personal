// @vitest-environment jsdom
//
// #162 / #163 — the truth about a credential has to reach the screen.
//
// A bearer token typed into a header the server never reads (api_key) used to
// pass "Test connection" with a green tick, because the server accepted the
// handshake anonymously. The page must now (a) warn as soon as the header is
// not Authorization and (b) show the test result in amber, in words, when the
// gateway says the credential could not be verified.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

let cmp = null
let target = null

function stubGateway() {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const u = String(url && url.url ? url.url : url)
    let body = '{}'
    if (u.includes('/mcp/test')) body = JSON.stringify({
      ok: true, tools: 117, credential_verified: false,
      message: 'Reachable (117 tools), but this server accepts the handshake without a credential, so yours could not be verified — it is only checked on the first tool call. Make sure the header name is what the server expects (usually Authorization).',
    })
    else if (u.includes('/mcp')) body = JSON.stringify({ servers: [] })
    return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } })
  }))
}

const tick = (ms = 30) => new Promise(r => setTimeout(r, ms))

async function mountPage() {
  const { default: MCP } = await import('./MCP.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  cmp = new MCP({ target })
  await tick(40)
}

function setSelect(select, value) {
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
}
function setInput(input, value) {
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
}
const text = () => target.textContent.replace(/\s+/g, ' ')

async function openBearerDialog() {
  const btn = [...target.querySelectorAll('button')].find(b => /New Server/i.test(b.textContent))
  btn.click()
  await tick()
  const selects = [...target.querySelectorAll('select')]
  setSelect(selects.find(s => [...s.options].some(o => o.value === 'http')), 'http')
  await tick()
  setSelect([...target.querySelectorAll('select')].find(s => [...s.options].some(o => o.value === 'bearer')), 'bearer')
  await tick()
  setInput(target.querySelector('input[placeholder="https://example.com/mcp"]'), 'https://mcp.equibles.com/mcp')
  await tick()
}

describe('MCP page: credential truth', () => {
  beforeEach(stubGateway)
  afterEach(() => {
    if (cmp) cmp.$destroy()
    if (target) target.remove()
    cmp = null
    target = null
    vi.unstubAllGlobals()
  })

  it('warns when a bearer token is aimed at a header other than Authorization', async () => {
    await mountPage()
    await openBearerDialog()
    const header = target.querySelector('input[placeholder="Authorization"]')
    expect(header).toBeTruthy()
    expect(text()).not.toContain('almost always sent as')

    setInput(header, 'api_key')
    await tick()
    expect(text()).toContain('almost always sent as')

    setInput(header, 'Authorization')
    await tick()
    expect(text()).not.toContain('almost always sent as')
  })

  it('shows an unverified credential in amber, in words', async () => {
    await mountPage()
    await openBearerDialog()
    setInput(target.querySelector('input[placeholder="Paste token or API key"]'), 'eq_test')
    await tick()
    const test = [...target.querySelectorAll('button')].find(b => /Test connection/i.test(b.textContent))
    test.click()
    await tick(60)
    const result = target.querySelector('.test-result')
    expect(result).toBeTruthy()
    expect(result.classList.contains('warn')).toBe(true)
    expect(result.classList.contains('ok')).toBe(false)
    expect(result.textContent).toContain('⚠')
    expect(result.textContent).toContain('could not be verified')
  })
})
