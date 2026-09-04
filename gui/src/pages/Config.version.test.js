// @vitest-environment jsdom
//
// The version has to be on the screen when you are UP TO DATE.
//
// It was rendered only inside `{#if updateInfo.update_available}`, so the one
// state where it never appeared was the normal one. Someone wanting to know
// what they were running — before filing a bug, after an upgrade, to check a
// release landed — had to fall back to `soulacy --version`, a curl at /health,
// or grepping the startup log.
//
// A unit test on versionSummary cannot catch that: the helper was fine, the
// markup was what hid it. So this mounts the real page and looks for the
// number, with the gateway reporting no update available.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

let cmp = null
let target = null

/** Answer /system/updates/status with `status`; everything else gets {}. */
function stubGateway(status) {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const u = String(url && url.url ? url.url : url)
    const body = u.includes('/system/updates/status') ? JSON.stringify(status) : '{}'
    return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } })
  }))
}

async function mount() {
  const { default: Config } = await import('./Config.svelte')
  target = document.createElement('div')
  document.body.appendChild(target)
  cmp = new Config({ target })
  await new Promise((r) => setTimeout(r, 40))
  return target.textContent.replace(/\s+/g, ' ')
}

beforeEach(() => { localStorage.clear() })
afterEach(() => {
  if (cmp) cmp.$destroy()
  if (target) target.remove()
  cmp = null; target = null
  vi.unstubAllGlobals()
})

describe('the Config page tells you what you are running', () => {
  it('shows the version when there is no update available', async () => {
    stubGateway({ current_version: 'v0.1.5', latest_version: '0.1.5', update_available: false })
    const text = await mount()
    expect(text).toContain('v0.1.5')
    expect(text).toMatch(/up to date/i)
  })

  it('still shows the version you are on when you are behind', async () => {
    stubGateway({ current_version: 'v0.1.3', latest_version: '0.1.5', update_available: true })
    const text = await mount()
    expect(text).toContain('v0.1.3')
    expect(text).toContain('0.1.5')
  })

  it('offers a way to re-check without leaving the page', async () => {
    stubGateway({ current_version: 'v0.1.5', update_available: false })
    await mount()
    const btn = [...target.querySelectorAll('button')]
      .find((b) => /check for updates/i.test(b.textContent || ''))
    expect(btn, 'no "Check for updates" button on the Config page').toBeTruthy()
  })
})
