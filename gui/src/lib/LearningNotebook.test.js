// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, unmount } from 'svelte'
import LearningNotebook from './LearningNotebook.svelte'
import { apiFetch } from './api.js'
import { apiKey } from './stores.js'
import { validateLesson, validateLessons } from './learningNotebook.js'
vi.mock('./api.js', () => ({ apiFetch: vi.fn() }))
const lesson = (status = 'pending') => ({ id: 'one', agent_id: 'agent', key: 'release-checks', kind: 'skill', title: 'Release checklist', trigger: 'Preparing a release', content: '<script>bad()</script> Check the staging migration.', verification: 'Run smoke tests.', pitfalls: 'Stop on a mismatch.', status, version: 1, uses: 0, helpful: 0, unhelpful: 0, citations: [{ source_id: 'user', quote: 'Always check staging first.' }], sources: [{ id: 'user', kind: 'user', text: 'Always check staging first.' }] })
const page = () => ({ lessons: [lesson()], enabled: true, auto_propose: true, total: 1, offset: 0 })
let component, target
const wait = () => new Promise(resolve => setTimeout(resolve, 0))
const button = name => [...target.querySelectorAll('button')].find(b => b.textContent.trim().startsWith(name))
async function render() { target = document.createElement('div'); document.body.append(target); component = mount(LearningNotebook, { target, props: { agentID: 'agent' } }); await wait() }
async function open() { apiFetch.mockResolvedValueOnce(page()).mockResolvedValueOnce(lesson()); await render(); button('Release checklist').click(); await wait() }
function acknowledge() { const box = target.querySelector('.ack input'); box.checked = true; box.dispatchEvent(new Event('change', { bubbles: true })) }
afterEach(async () => { if (component) await unmount(component); component = null; target?.remove(); vi.resetAllMocks() })

describe('Learning notebook', () => {
  it.each(['foreign-agent', 'foreign-id', 'status', 'kind', 'large', 'missing-source', 'forged-quote', 'negative-count', 'duplicate-id'])('rejects %s', kind => {
    const l = lesson()
    if (kind === 'foreign-agent') l.agent_id = 'other'
    if (kind === 'foreign-id') l.id = 'other'
    if (kind === 'status') l.status = 'learned'
    if (kind === 'kind') l.kind = 'execute'
    if (kind === 'large') l.content = 'x'.repeat(6001)
    if (kind === 'missing-source') l.sources = []
    if (kind === 'forged-quote') l.citations[0].quote = 'invented'
    if (kind === 'negative-count') l.uses = -1
    if (kind === 'duplicate-id') expect(() => validateLessons({ ...page(), lessons: [l, l] }, 'agent')).toThrow()
    else expect(() => validateLesson(l, 'agent', 'one')).toThrow()
  })
  it('shows inert exact text and requires acknowledgement', async () => {
    await open()
    expect(target.querySelector('script')).toBeNull()
    expect(target.textContent).toContain('<script>bad()</script>')
    expect(target.textContent).toContain('Always check staging first.')
    expect(button('Approve lesson').disabled).toBe(true)
    expect(apiFetch).toHaveBeenCalledTimes(2)
    acknowledge(); await wait(); expect(button('Approve lesson').disabled).toBe(false)
    apiFetch.mockResolvedValueOnce(lesson('active')); button('Approve lesson').click(); await wait()
    expect(JSON.parse(apiFetch.mock.calls[2][1].body)).toEqual({ action: 'approve', confirmed: true })
    expect(target.textContent).toContain('Available in future runs')
  })
  it('does not retry a lost acknowledgement or retain stale approval', async () => {
    await open(); acknowledge(); await wait(); apiFetch.mockRejectedValueOnce(new Error('Network lost'))
    button('Approve lesson').click(); await wait()
    expect(apiFetch).toHaveBeenCalledTimes(3); expect(button('Approve lesson')).toBeUndefined()
    expect(target.textContent).toContain('Refresh before trying again')
  })
  it('does not claim approval when the server returns a pending record', async () => {
    await open(); acknowledge(); await wait(); apiFetch.mockResolvedValueOnce(lesson())
    button('Approve lesson').click(); await wait()
    expect(target.textContent).not.toContain('Available in future runs')
    expect(target.textContent).toContain('The server did not confirm this change')
    expect(button('Approve lesson')).toBeUndefined()
  })
  it('does not claim a new draft when teach returns an active record', async () => {
    apiFetch.mockResolvedValueOnce(page()).mockResolvedValueOnce({ lesson: lesson('active'), created: true }); await render()
    const area = target.querySelector('textarea'); area.value = 'Use staging for release checks.'; area.dispatchEvent(new Event('input', { bubbles: true })); await wait()
    button('Create learning draft').click(); await wait()
    expect(target.textContent).not.toContain('Draft ready')
    expect(target.textContent).toContain('did not confirm a reviewable draft')
  })
  it('does not restore private state after sign-in changes during a read', async () => {
    let resolve
    apiFetch.mockResolvedValueOnce(page()).mockImplementationOnce(() => new Promise(r => { resolve = r }))
    await render(); button('Release checklist').click(); await wait()
    apiKey.set('different-user'); await wait(); resolve(lesson()); await wait()
    expect(target.textContent).toContain('Sign-in changed')
    expect(target.textContent).not.toContain('Always check staging first.')
    expect(button('Approve lesson')).toBeUndefined()
  })
  it('requires the previous revision before enabling a correction review', async () => {
    const l = { ...lesson(), base_id: 'previous', version: 2 }
    apiFetch.mockResolvedValueOnce(page()).mockResolvedValueOnce(l).mockRejectedValueOnce(new Error('Previous version unavailable'))
    await render(); button('Release checklist').click(); await wait(); await wait()
    expect(button('Approve lesson')).toBeUndefined()
    expect(target.textContent).toContain('Previous version unavailable')
  })
  it('cannot teach while disabled or with an oversized note', async () => {
    apiFetch.mockResolvedValueOnce({ ...page(), enabled: false }); await render()
    const area = target.querySelector('textarea'); area.value = 'Learn a useful procedure'; area.dispatchEvent(new Event('input', { bubbles: true })); await wait()
    expect(button('Create learning draft').disabled).toBe(true)
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })
  it('displays a null lesson as no learning, not a success', async () => {
    apiFetch.mockResolvedValueOnce(page()).mockResolvedValueOnce({ lesson: null, created: false }); await render()
    const area = target.querySelector('textarea'); area.value = 'Hello there'; area.dispatchEvent(new Event('input', { bubbles: true })); await wait()
    button('Create learning draft').click(); await wait()
    expect(target.textContent).toContain('No reusable lesson was found')
    expect(button('Approve lesson')).toBeUndefined()
  })
})
