// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, unmount } from 'svelte'
import SafeUndo from './SafeUndo.svelte'
import { apiFetch } from './api.js'
import { apiKey } from './stores.js'
import { validateJob, validateJobs, validateReview, directions, displayValue } from './safeUndo.js'
vi.mock('./api.js', () => ({ apiFetch: vi.fn() }))
const value = display => ({ exists: true, display })
const job = () => ({ id: 'receipt', agent_id: 'agent', title: 'Update customer', status: 'draft', undo_started: false, updated_at: new Date().toISOString(), actions: [
  { resource_id: 'record', name: 'Customer', kind: 'json_record', status: 'pending', reconciled: false, fields: [{ name: 'balance', before: value('9007199254740993123456789'), after: value('9007199254740993123456790') }] }
] })
const review = (j = job(), direction = 'apply') => ({ job: j, token: 'single-use-token', direction, expires_at: new Date(Date.now() + 299000).toISOString(), warning: 'Cross-system changes are not atomic.', steps: [{ index: 0, result: direction === 'undo' ? 'undone' : 'applied' }] })
const list = () => ({ jobs: [{ ...job(), action_count: 1 }] })
const resources = { resources: [{ id: 'record', agent_id: 'agent', name: 'Customer', kind: 'json_record' }] }
let component, target
const wait = () => new Promise(resolve => setTimeout(resolve, 0))
const button = label => [...target.querySelectorAll('button')].find(b => b.textContent.trim().startsWith(label))
async function render() {
  target = document.createElement('div'); document.body.append(target)
  component = mount(SafeUndo, { target, props: { agentID: 'agent' } }); await wait()
}
async function renderReview(r = review()) {
  apiFetch.mockResolvedValueOnce(list()).mockResolvedValueOnce(resources).mockResolvedValueOnce(r.job).mockResolvedValueOnce(r)
  await render(); button('Update customer').click(); await wait(); button('Preview:').click(); await wait()
}
afterEach(async () => { if (component) await unmount(component); component = null; target?.remove(); vi.restoreAllMocks(); vi.resetAllMocks() })

describe('Safe Undo review contract', () => {
  it('keeps large numbers exact and null distinct from a missing field', () => {
    expect(validateJob(job(), 'agent', 'receipt')).toBeTruthy()
    expect(displayValue(job().actions[0].fields[0].before)).toBe('9007199254740993123456789')
    expect(displayValue(value('null'))).toBe('null')
    expect(displayValue({ exists: false })).toBe('(field absent)')
  })
  it.each(['foreign-agent', 'foreign-id', 'unknown-status', 'unknown-kind', 'oversize', 'duplicate-resource', 'duplicate-field', 'missing-value'])("rejects %s", kind => {
    const j = job()
    if (kind === 'foreign-agent') j.agent_id = 'other'
    if (kind === 'foreign-id') j.id = 'other'
    if (kind === 'unknown-status') j.status = 'success'
    if (kind === 'unknown-kind') j.actions[0].kind = 'send_email'
    if (kind === 'oversize') j.actions[0].fields[0].after.display = 'x'.repeat(65537)
    if (kind === 'duplicate-resource') j.actions.push(j.actions[0])
    if (kind === 'duplicate-field') j.actions[0].fields.push(j.actions[0].fields[0])
    if (kind === 'missing-value') delete j.actions[0].fields[0].after.display
    expect(() => validateJob(j, 'agent', 'receipt')).toThrow('invalid')
  })
  it('rejects mismatched, expired, missing and forged review steps', () => {
    for (const patch of [{ direction: 'undo' }, { expires_at: new Date(0).toISOString() }, { token: '' }, { steps: [] }, { steps: [{ index: 8, result: 'applied' }] }, { steps: [{ index: 0, result: 'undone' }] }, { steps: [{ index: 0, result: 'applied' }, { index: 0, result: 'applied' }] }]) {
      expect(() => validateReview({ ...review(), ...patch }, 'agent', 'receipt', 'apply')).toThrow('invalid')
    }
  })
  it('requires reverse order for multi-system undo and exposes reconciliation observations', () => {
    const j = job(); j.status = 'applied'; j.actions[0].status = 'applied'
    j.actions.push({ ...j.actions[0], resource_id: 'other' })
    const r = { ...review(j, 'undo'), steps: [{ index: 1, result: 'undone' }, { index: 0, result: 'undone' }] }
    expect(validateReview(r, 'agent', 'receipt', 'undo')).toBe(r)
    expect(() => validateReview({ ...r, steps: r.steps.toReversed() }, 'agent', 'receipt', 'undo')).toThrow()
    j.actions = [j.actions[0]]; j.actions[0].status = 'needs_review'
    expect(directions(j)).toEqual(['reconcile'])
    expect(validateReview({ ...review(j, 'reconcile'), steps: [{ index: 0, result: 'pending' }] }, 'agent', 'receipt', 'reconcile')).toBeTruthy()
  })
  it('rejects excessive and duplicate lists and never reoffers apply after undo started', () => {
    expect(() => validateJobs({ jobs: [list().jobs[0], list().jobs[0]] }, 'agent')).toThrow()
    expect(() => validateJobs({ jobs: Array(101).fill(list().jobs[0]) }, 'agent')).toThrow()
    expect(directions({ ...job(), undo_started: true })).toEqual([])
  })
})

describe('Safe Undo screen', () => {
  it('requires an explicit review and acknowledgement and suppresses double submission', async () => {
    await renderReview()
    const confirm = button('Confirm:')
    expect(confirm.disabled).toBe(true)
    confirm.click(); expect(apiFetch).toHaveBeenCalledTimes(4)
    target.querySelector('input[type=checkbox]').click(); await wait()
    let resolve
    apiFetch.mockReturnValueOnce(new Promise(r => { resolve = r }))
    confirm.click(); confirm.click(); await wait()
    expect(apiFetch).toHaveBeenCalledTimes(5)
    expect(JSON.parse(apiFetch.mock.calls[4][1].body)).toEqual({ token: 'single-use-token', confirmed: true })
    const applied = job(); applied.status = 'applied'; applied.actions[0].status = 'applied'
    resolve(applied); await wait()
    expect(button('Preview: Undo changes')).toBeTruthy()
    expect(button('Confirm:')).toBeUndefined()
  })
  it('does not replay after a lost response and requires reopening the canonical receipt', async () => {
    await renderReview()
    apiFetch.mockRejectedValueOnce(new Error('Connection lost'))
    target.querySelector('input').click(); await wait(); button('Confirm:').click(); await wait()
    expect(target.textContent).toContain('No automatic retry')
    expect(target.querySelector('[aria-label="Change receipt"]')).toBeNull()
    expect(apiFetch).toHaveBeenCalledTimes(5)
  })
  it('discards private values and approval tokens after sign-in changes', async () => {
    await renderReview()
    apiKey.set('safe-undo-other-principal'); await wait()
    expect(target.textContent).not.toContain('9007199254740993')
    expect(target.textContent).toContain('sign-in changed')
    expect(button('Confirm:')).toBeUndefined()
  })
  it('does not restore old identity state from a delayed network response', async () => {
    let resolve
    apiFetch.mockReturnValueOnce(new Promise(r => { resolve = r })).mockResolvedValueOnce(resources)
    await render(); apiKey.set('safe-undo-new-principal'); resolve(list()); await wait()
    expect(button('Update customer')).toBeUndefined()
  })
  it('renders external markup as text without executing or fetching it', async () => {
    const j = job(); j.actions[0].fields[0].after.display = '"<img src=https://example.com/leak onerror=alert(1)>"'
    await renderReview(review(j))
    expect(target.querySelector('pre').textContent).toContain('9007199254740993')
    expect(target.textContent).toContain('<img')
    expect(target.querySelector('img')).toBeNull()
  })
  it('requires explicit cancellation without issuing a write', async () => {
    await renderReview(); button('Cancel preview').click(); await wait()
    expect(button('Confirm:')).toBeUndefined()
    expect(apiFetch).toHaveBeenCalledTimes(4)
  })
  it('rejects confirmation when the preview expires between rendering and clicking', async () => {
    await renderReview(); target.querySelector('input').click(); await wait()
    vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 400000)
    button('Confirm:').click(); await wait()
    expect(apiFetch).toHaveBeenCalledTimes(4)
  })
  it('shows unsupported configuration honestly', async () => {
    apiFetch.mockResolvedValueOnce({ jobs: [] }).mockResolvedValueOnce({ resources: [] })
    await render(); expect(target.textContent).toContain('server owner must configure')
    expect(button('Confirm:')).toBeUndefined()
  })
})
