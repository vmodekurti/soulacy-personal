// @vitest-environment jsdom
//
// Upload auth files (e.g. cookies.txt) to an MCP server from the edit modal:
// the section appears for an existing server, lists its files, uploads, and
// deletes — talking to /api/v1/mcp/:id/files.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

let cmp = null
let target = null
let calls = []

function stubGateway() {
  calls = []
  vi.stubGlobal('confirm', () => true)
  vi.stubGlobal('fetch', vi.fn(async (url, opts = {}) => {
    const u = String(url && url.url ? url.url : url)
    const method = (opts.method || 'GET').toUpperCase()
    calls.push(`${method} ${u}`)
    const json = (obj) => new Response(JSON.stringify(obj), { status: 200, headers: { 'content-type': 'application/json' } })

    if (u.includes('/files')) {
      if (method === 'POST') return json({ files: [{ name: 'existing.txt', size: 10 }, { name: 'cookies.txt', size: 11 }], dir_env: 'SOULACY_MCP_FILE_DIR' })
      if (method === 'DELETE') return json({ files: [{ name: 'existing.txt', size: 10 }], dir_env: 'SOULACY_MCP_FILE_DIR' })
      return json({ files: [{ name: 'existing.txt', size: 10 }], dir_env: 'SOULACY_MCP_FILE_DIR' })
    }
    if (u.endsWith('/mcp')) {
      return json({ servers: [{ id: 'notebooklm', transport: 'stdio', command: 'echo', args: [], tools: [], connected: true }] })
    }
    return json({})
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

const text = () => target.textContent.replace(/\s+/g, ' ')

async function openEditModal() {
  const edit = [...target.querySelectorAll('button')].find(b => /^Edit$/.test(b.textContent.trim()))
  edit.click()
  await tick(40)
}

describe('MCP page: per-server file uploads', () => {
  beforeEach(stubGateway)
  afterEach(() => {
    if (cmp) cmp.$destroy()
    if (target) target.remove()
    cmp = null
    target = null
    vi.unstubAllGlobals()
  })

  it('shows the file section with existing files when editing a saved server', async () => {
    await mountPage()
    await openEditModal()
    expect(text()).toContain('Authentication files')
    expect(text()).toContain('existing.txt')
    expect(calls.some(c => c === 'GET /api/v1/mcp/notebooklm/files')).toBe(true)
  })

  it('uploads a selected file and refreshes the list', async () => {
    await mountPage()
    await openEditModal()
    const input = target.querySelector('input[type=file]')
    expect(input).toBeTruthy()
    const file = new File(['session=abc'], 'cookies.txt', { type: 'text/plain' })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await tick(40)
    expect(calls.some(c => c === 'POST /api/v1/mcp/notebooklm/files')).toBe(true)
    expect(text()).toContain('cookies.txt')
  })

  it('deletes a file', async () => {
    await mountPage()
    await openEditModal()
    const del = [...target.querySelectorAll('button')].find(b => /^Remove$/.test(b.textContent.trim()))
    expect(del).toBeTruthy()
    del.click()
    await tick(40)
    expect(calls.some(c => c.startsWith('DELETE /api/v1/mcp/notebooklm/files/'))).toBe(true)
  })
})
