// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, unmount } from 'svelte'
import ModelPreparation from './ModelPreparation.svelte'
import { apiFetch } from './api.js'
import { apiKey } from './stores.js'
import { validatePreparation, supportLabel } from './modelPreparation.js'
vi.mock('./api.js', () => ({ apiFetch: vi.fn() }))
const fixture = () => ({ agent_id: 'agent', goal_preserved: true, strategy: 'react', max_output_tokens: 1024, approach: ['Keep requirements intact.'], warnings: ['Not a benchmark.'], profile: { provider: 'ollama', model: '<script>unsafe()</script>', source: 'provider_metadata', context_source: 'configured', context_tokens: 16384, input_tokens: 0, output_tokens: 0, chat: 'supported', native_tools: 'unsupported', json_mode: 'supported', reasoning: 'unknown', vision: 'unknown' } })
let component, target
const wait = () => new Promise(resolve => setTimeout(resolve, 0))
async function render() { target = document.createElement('div'); document.body.append(target); component = mount(ModelPreparation, { target, props: { agentID: 'agent' } }); await wait() }
afterEach(async () => { if (component) await unmount(component); component = null; target?.remove(); vi.resetAllMocks() })
describe('Model preparation', () => {
  it('shows evidence without executing markup or making a write', async () => {
    apiFetch.mockResolvedValue(fixture()); await render()
    expect(target.textContent).toContain('Guarded step-by-step')
    expect(target.textContent).toContain('<script>unsafe()</script>')
    expect(target.querySelector('script')).toBeNull()
    expect(target.textContent).toContain('Not reported')
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith('/agents/agent/model-preparation')
  })
  it('discards an in-flight response after sign-in changes', async () => {
    let resolve; apiFetch.mockReturnValue(new Promise(r => { resolve = r })); await render()
    apiKey.set('different-identity'); resolve(fixture()); await wait()
    expect(target.textContent).toContain('Sign-in changed')
    expect(target.textContent).not.toContain('unsafe()')
  })
  it('handles an older gateway without claiming support', async () => {
    apiFetch.mockRejectedValue({ status: 404 }); await render()
    expect(target.textContent).toContain('unavailable on this gateway')
  })
  it.each(['agent', 'goal', 'limit', 'capability', 'source', 'approach'])('rejects malformed %s metadata', key => {
    const value = fixture()
    if (key === 'agent') value.agent_id = 'foreign'
    if (key === 'goal') value.goal_preserved = false
    if (key === 'limit') value.profile.context_tokens = -1
    if (key === 'capability') value.profile.native_tools = 'guaranteed'
    if (key === 'source') value.profile.source = 'best-model'
    if (key === 'approach') value.approach = [false]
    expect(() => validatePreparation(value, 'agent')).toThrow()
  })
  it('never labels an unknown capability as unsupported', () => { expect(supportLabel('unknown')).toBe('Not reported') })
})
