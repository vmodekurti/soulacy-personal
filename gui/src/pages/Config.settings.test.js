// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import Config from './Config.svelte'

const fixture = {
  _meta: { writable: true },
  runtime: { default_max_turns: 0, default_budget: { max_tokens: 0, max_llm_calls: 0 } },
  llm: { default_provider: 'ollama', providers: { ollama: {}, openai: {} }, studio: { provider: 'openai', model: 'custom', max_build_tokens: 88000, max_build_cost_usd: 8 } },
  memory: { adaptive: { enabled: true, prompt_token_budget: 0, min_confidence: 0 } },
  costs: { daily_budget_usd: 19, per_user_daily_budget_usd: 3, default_max_output_tokens: 32768, pricing: { 'a/b': { input_per_mtok: 2, cached_input_per_mtok: 1, source: 'manual' } } },
  updates: { auto: false, check_interval: '6h0m0s' },
}
let target, component, patches
const settle = async () => { await tick(); await new Promise(resolve => setTimeout(resolve, 40)) }
const click = async text => {
  const button = [...target.querySelectorAll('button')].find(node => node.textContent.trim() === text)
  expect(button).toBeTruthy()
  button.click(); await settle()
}
const input = async (selector, value, event = 'input') => {
  const node = target.querySelector(selector)
  node.value = value
  node.dispatchEvent(new Event(event, { bubbles: true }))
  await settle()
}
beforeEach(async () => {
  patches = []
  localStorage.clear()
  vi.stubGlobal('fetch', vi.fn(async (url, options = {}) => {
    const path = String(url)
    let body = {}
    if (path.endsWith('/config')) {
      if (options.method === 'PATCH') { patches.push(JSON.parse(options.body)); body = { config: fixture } }
      else body = fixture
    } else if (path.includes('/system/updates/status')) body = { current_version: 'test', update_available: false }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
  }))
  target = document.createElement('div'); document.body.appendChild(target)
  component = new Config({ target }); await settle()
})
afterEach(() => { component.$destroy(); target.remove(); vi.unstubAllGlobals() })

describe('everyday and advanced settings', () => {
  it('starts simple, exposes advanced controls on demand, and retains edits across views', async () => {
    expect(target.querySelector('#default-provider')).toBeTruthy()
    expect(target.querySelector('#search-provider')).toBeTruthy()
    expect(target.querySelector('#max-turns')).toBeNull()
    expect(target.querySelector('#adaptive-token-budget')).toBeNull()
    await click('Advanced')
    expect(target.querySelector('#max-turns').value).toBe('0')
    await input('#max-turns', '512')
    await click('Everyday'); await click('Advanced')
    expect(target.querySelector('#max-turns').value).toBe('512')
    await click('Everyday'); await click('Save changes')
    expect(patches).toEqual([{ runtime: { default_max_turns: 512 } }])
  })
  it('saves a basic provider change without rewriting limits, model roles or memory', async () => {
    await input('#default-provider', 'openai', 'change')
    await click('Save changes')
    expect(patches).toEqual([{ llm: { default_provider: 'openai' } }])
    await click('Save changes')
    expect(patches).toHaveLength(1)
  })
  it('saves the memory toggle without turning zero-valued advanced options into defaults', async () => {
    const checkbox = [...target.querySelectorAll('input[type="checkbox"]')].find(node => node.parentElement.textContent.includes('Remember useful'))
    checkbox.click(); await settle(); await click('Save changes')
    expect(patches).toEqual([{ memory: { adaptive: { enabled: false } } }])
  })
  it('keeps Studio build allowances when a role is changed', async () => {
    await click('Advanced')
    await input('#studio-provider', '', 'change'); await click('Save changes')
    expect(patches[0].llm.studio).toMatchObject({ provider: '', max_build_tokens: 88000, max_build_cost_usd: 8 })
    expect(patches[0].costs).toBeUndefined()
  })
  it('preserves hidden cost policies and price metadata when editing prices', async () => {
    await click('Advanced')
    await input('.cost-row .cost-rate input', '7')
    await click('Save changes')
    expect(patches[0].costs).toMatchObject({ per_user_daily_budget_usd: 3, default_max_output_tokens: 32768, pricing: { 'a/b': { input_per_mtok: 7, cached_input_per_mtok: 1, source: 'manual' } } })
  })
})
