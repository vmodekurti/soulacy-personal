// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, unmount } from 'svelte'
import PublishedFiles from './PublishedFiles.svelte'
import { apiFetch } from './api.js'
import { apiKey } from './stores.js'
vi.mock('./api.js', () => ({ apiFetch: vi.fn() }))

let component, target
const entry = { name: 'hello #?%.html', path: 'hello #?%.html', kind: 'file', size_bytes: 12, previewable: true }
const listing = { path: '', entries: [entry], read_only: true, truncated: false }
const wait = () => new Promise(resolve => setTimeout(resolve, 0))
async function render() {
  target = document.createElement('div'); document.body.append(target)
  component = mount(PublishedFiles, { target, props: { agentID: 'agent' } }); await wait()
}
afterEach(async () => { if (component) await unmount(component); component = null; target?.remove(); vi.resetAllMocks() })

describe('Published files', () => {
  it('loads read-only files and displays HTML as inert text', async () => {
    apiFetch.mockResolvedValueOnce(listing).mockResolvedValueOnce({ path: entry.path, read_only: true, size_bytes: 38, content: '<img src="/leak" onerror="pwned=1">' })
    await render()
    target.querySelector('.file-row').click(); await wait()
    expect(apiFetch.mock.calls[1][0]).toContain('path=hello+%23%3F%25.html')
    expect(target.querySelector('pre').textContent).toContain('<img')
    expect(target.querySelector('img')).toBeNull()
    expect(apiFetch.mock.calls.every(c => c.length === 1)).toBe(true)
  })
  it('shows missing configuration instead of inventing files', async () => {
    apiFetch.mockRejectedValue(new Error('No published folder is configured'))
    await render()
    expect(target.querySelector('[role=alert]').textContent).toContain('No published folder')
    expect(target.querySelector('.file-row')).toBeNull()
  })
  it('rejects duplicate, foreign-folder and non-read-only listings', async () => {
    for (const invalid of [{ ...listing, entries: [entry, entry] }, { ...listing, path: 'foreign' }, { ...listing, read_only: false }, { ...listing, entries: [{ ...entry, path: '../secret' }] }]) {
      apiFetch.mockResolvedValueOnce(invalid)
      await render()
      expect(target.querySelector('[role=alert]').textContent).toContain('invalid folder')
      expect(target.querySelector('.file-row')).toBeNull()
      await unmount(component); component = null; target.remove()
    }
  })
  it('keeps unsupported previews disabled and announces truncation', async () => {
    apiFetch.mockResolvedValue({ ...listing, truncated: true, entries: [{ ...entry, previewable: false }] })
    await render()
    expect(target.querySelector('.file-row').disabled).toBe(true)
    expect(target.textContent).toContain('too large to show in full')
  })
  it('does not resurrect state after the component is removed', async () => {
    let resolve
    apiFetch.mockReturnValue(new Promise(r => { resolve = r }))
    await render()
    await unmount(component); component = null
    resolve(listing); await wait()
    expect(target.querySelector('.file-row')).toBeNull()
  })
  it('clears private previews when gateway sign-in changes', async () => {
    apiFetch.mockResolvedValueOnce(listing).mockResolvedValueOnce({ path: entry.path, read_only: true, content: 'private report', size_bytes: 14 })
    await render()
    target.querySelector('.file-row').click(); await wait()
    expect(target.textContent).toContain('private report')
    apiKey.set('another-test-principal'); await wait()
    expect(target.textContent).not.toContain('private report')
    expect(target.textContent).toContain('sign-in changed')
    apiKey.set('')
  })
})
