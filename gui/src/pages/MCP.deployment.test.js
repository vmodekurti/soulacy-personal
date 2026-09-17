// @vitest-environment jsdom
//
// The limits have to reach the screen.
//
// A unit test on the helpers cannot catch the failure that matters here: the
// helper returning "this deployment cannot drive a browser" is worth nothing
// if the markup never renders it, and that is precisely the shape of bug this
// codebase has shipped before — a working helper behind a condition that was
// never true. So this mounts the real page against a gateway that reports a
// deployment with limits, and looks for the words a user would need to read.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

let cmp = null
let target = null

const REPORT = {
  deployment: {
    platform: 'Railway',
    platform_kind: 'paas',
    workspace: '/data/soulspace',
    capabilities: [
      { id: 'agent_shell', name: 'Agents can run shell commands', available: false,
        detail: 'no agent holds the system grant',
        workaround: 'install Skills and MCP servers with package_install' },
      { id: 'node_runtime', name: 'Node-based MCP servers', available: true, detail: 'v20.20.2' },
      { id: 'browser_automation', name: 'Browser automation', available: false,
        detail: 'the image does not carry Chromium libraries',
        workaround: 'use --cdp-endpoint to drive a remote browser' },
    ],
  },
}

function stubGateway() {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const u = String(url && url.url ? url.url : url)
    let body = '{}'
    if (u.includes('/doctor')) body = JSON.stringify(REPORT)
    else if (u.includes('/mcp')) body = JSON.stringify({ servers: [] })
    return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } })
  }))
}

/** Click "+ New Server" and return what the dialog shows. */
async function openNewServerDialog() {
  const btn = [...target.querySelectorAll('button')].find(b => /New Server/i.test(b.textContent))
  if (!btn) throw new Error('no "New Server" button on the page')
  btn.click()
  await new Promise(r => setTimeout(r, 30))
  return target.textContent.replace(/\s+/g, ' ')
}

async function mountPage() {
  const { default: MCP } = await import('./MCP.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  cmp = new MCP({ target })
  // Let the two onMount fetches settle.
  await new Promise(r => setTimeout(r, 40))
  return target.textContent.replace(/\s+/g, ' ')
}

describe('MCP page: what this deployment cannot do', () => {
  beforeEach(stubGateway)
  afterEach(() => {
    if (cmp) cmp.$destroy()
    if (target) target.remove()
    cmp = null
    target = null
    vi.unstubAllGlobals()
  })

  it('names the limits and the way around each one', async () => {
    const text = await mountPage()
    expect(text).toContain('2 things this deployment cannot do')
    expect(text).toContain('Agents can run shell commands')
    expect(text).toContain('package_install')
    expect(text).toContain('--cdp-endpoint')
  })

  it('says where it is running, so the limits make sense', async () => {
    const text = await mountPage()
    expect(text).toContain('Railway')
  })

  // The templates live in the new-server dialog, which is where someone is
  // about to choose one — so that is where the warning has to be.
  it('warns, in the dialog, that some templates will not run here', async () => {
    await mountPage()
    const text = await openNewServerDialog()
    expect(text).toContain('need something this deployment does not have')
  })

  it('still offers the remote browser template, which needs nothing local', async () => {
    await mountPage()
    const text = await openNewServerDialog()
    expect(text).toContain('Browser remote (CDP)')
  })
})
