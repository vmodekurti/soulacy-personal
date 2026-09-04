// @vitest-environment jsdom
//
// Every nav page must survive being constructed and mounted.
//
// Svelte 4 has no error boundary. When a page component throws while Svelte is
// flushing — which is exactly what `<svelte:component>` does on every route
// change — the exception escapes flush() and the scheduler is never rescheduled.
// From then on the whole SPA stops rendering: clicking a nav item still changes
// the URL, but nothing on screen updates and only a reload recovers. One bad
// page takes down all twenty-two.
//
// That is not a failure mode worth discovering by hand, so this mounts each page
// with a stubbed gateway and fails the build if any of them throws.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { navPages } from '../lib/nav.js'

// Mirrors App.svelte's pageLoaders. Static import() specifiers so the bundler
// can resolve them; steps.test.js already guards the nav list itself.
const loaders = {
  dashboard: () => import('./Dashboard.svelte'),
  onboarding: () => import('./Onboarding.svelte'),
  studio: () => import('./Studio.svelte'),
  agents: () => import('./Agents.svelte'),
  templates: () => import('./Templates.svelte'),
  chat: () => import('./Chat.svelte'),
  memory: () => import('./Memory.svelte'),
  knowledge: () => import('./Knowledge.svelte'),
  queues: () => import('./Queues.svelte'),
  workboard: () => import('./Workboard.svelte'),
  channels: () => import('./Channels.svelte'),
  schedule: () => import('./Schedule.svelte'),
  skills: () => import('./Skills.svelte'),
  mcp: () => import('./MCP.svelte'),
  pluginmgr: () => import('./PluginManager.svelte'),
  providers: () => import('./Providers.svelte'),
  secrets: () => import('./Secrets.svelte'),
  activity: () => import('./Activity.svelte'),
  browser: () => import('./BrowserTrace.svelte'),
  config: () => import('./Config.svelte'),
  mobile: () => import('./Mobile.svelte'),
  logs: () => import('./Logs.svelte'),
}

// The freeze this file exists to catch does NOT arrive as a thrown exception
// the test can await. onMount kicks off a request; the response lands later and
// assigns state; Svelte schedules an update; the {#each} throws inside flush()
// on a microtask nobody is awaiting. Node reports it as an unhandled rejection
// and the test goes green.
//
// That is not hypothetical. Workboard did exactly this — `agents` was assigned
// a plain object because `{}` is truthy, `{#each agents}` threw, and all
// forty-five assertions here passed while vitest quietly printed "2 errors"
// underneath them. A smoke test whose entire subject is "does this page throw"
// must treat an async throw as a failure, or it is checking the one thing it
// was written to check in the one way that cannot detect it.
let asyncErrors = []
const captureRejection = (err) => { asyncErrors.push(err) }

beforeEach(() => {
  asyncErrors = []
  process.on('unhandledRejection', captureRejection)
  // Every gateway call answers with an empty object. A page that only works
  // when the server returns exactly the right shape is itself the bug.
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })))
})

afterEach(() => {
  process.off('unhandledRejection', captureRejection)
  vi.unstubAllGlobals()
})

/** Fail with the page's name and the original error, not just a stack. */
function expectNoAsyncThrow(id) {
  const messages = asyncErrors.map((e) => (e && e.message) || String(e))
  expect(
    messages,
    `${id} threw after mount, off the await chain. In the browser this escapes ` +
    `flush() and wedges Svelte's scheduler, so every page stops rendering ` +
    `until a reload:\n  ${messages.join('\n  ')}`,
  ).toEqual([])
}

describe('every nav page constructs and mounts', () => {
  it('has a loader for each nav entry', () => {
    const missing = navPages.map((p) => p.id).filter((id) => !loaders[id])
    expect(missing, `nav entries with no loader here: ${missing.join(', ')}`).toEqual([])
  })

  // Studio is ~9k lines and pulls in the flow canvas; the default 5s timeout is
  // not enough to transform and construct it on a cold run.
  it.each(navPages.map((p) => [p.id]))('%s', { timeout: 30000 }, async (id) => {
    const { default: Page } = await loaders[id]()
    const target = document.createElement('div')
    document.body.appendChild(target)
    let cmp = null
    expect(() => { cmp = new Page({ target }) }).not.toThrow()
    // onMount has fired by now; let its first round of requests settle so a
    // throw in the load path surfaces here too.
    await new Promise((r) => setTimeout(r, 30))
    if (cmp) cmp.$destroy()
    target.remove()
    expectNoAsyncThrow(id)
  })
})

// "Show me around" lives in the page now, not the sidebar — which means it has
// to be ON every page. A single missing call site is a screen with no way in,
// and there is nothing to notice it: the button's absence looks like a design
// choice rather than an omission.
describe('every nav page offers its own tour', () => {
  it.each(navPages.map((p) => [p.id]))('%s has a Show me around button', { timeout: 30000 }, async (id) => {
    const { default: Page } = await loaders[id]()
    const target = document.createElement('div')
    document.body.appendChild(target)
    const cmp = new Page({ target })
    await new Promise((r) => setTimeout(r, 30))

    const btn = [...target.querySelectorAll('button')]
      .find((b) => /show me around/i.test(b.textContent || ''))
    expect(btn, `${id} renders no "Show me around" button`).toBeTruthy()

    cmp.$destroy()
    target.remove()
  })
})
