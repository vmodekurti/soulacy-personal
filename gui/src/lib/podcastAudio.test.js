// @vitest-environment jsdom
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { podcastAudioRenderer } from './podcastAudio.js'
import { apiFetch } from './api.js'
vi.mock('./api.js', () => ({ apiFetch: vi.fn() }))

let renderer, node
const original = 'https://phone.example/api/podcasts/episodes/episode:abc/audio'
const flush = () => new Promise(resolve => setTimeout(resolve, 0))
beforeEach(() => {
  vi.resetAllMocks()
  window.location.hash = ''
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue()
  node = document.createElement('div')
  node.innerHTML = '<div class="md-audio-card"><a class="md-audio-link">Listen</a><audio></audio><a class="md-audio-original"></a></div>'
  node.querySelector('audio').dataset.podcastUrl = original
  node.querySelector('.md-audio-original').href = original
  document.body.appendChild(node)
  renderer = podcastAudioRenderer(node)
})
afterEach(() => { renderer.destroy(); node.remove(); vi.restoreAllMocks() })

describe('browser podcast playback', () => {
  it('uses authenticated same-origin playback while preserving the original link', async () => {
    apiFetch.mockResolvedValue({ url: '/api/v1/media/podcast?ticket=scoped', expires_at: Date.now()/1000+3600 })
    renderer.update()
    renderer.update()
    await flush()
    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({ url: original })
    expect(node.querySelector('audio').getAttribute('src')).toBe('/api/v1/media/podcast?ticket=scoped')
    expect(node.querySelector('.md-audio-original').href).toBe(original)
    node.querySelector('.md-audio-link').click()
    await flush()
    expect(HTMLMediaElement.prototype.play).toHaveBeenCalled()
  })
  it('keeps unrelated podcast services working', async () => {
    apiFetch.mockRejectedValue({ status: 404 })
    renderer.update(); await flush()
    expect(node.querySelector('audio').src).toBe(original)
  })
  it('provides a retry after a service failure', async () => {
    apiFetch.mockRejectedValueOnce({ status: 503 }).mockResolvedValueOnce({
      url: '/api/v1/media/podcast?ticket=retry', expires_at: Date.now()/1000+3600,
    })
    renderer.update(); await flush()
    expect(node.textContent).toContain('retry')
    node.querySelector('.md-audio-link').click(); await flush()
    expect(node.querySelector('audio').getAttribute('src')).toContain('ticket=retry')
    expect(node.querySelector('[role=status]').textContent).toBe('')
  })
  it('does not mutate removed players after a request resolves', async () => {
    let resolve
    apiFetch.mockReturnValue(new Promise(r => { resolve = r }))
    renderer.update()
    renderer.destroy()
    resolve({ url: '/api/v1/media/podcast?ticket=stale', expires_at: Date.now()/1000+3600 })
    await flush()
    expect(node.querySelector('audio').hasAttribute('src')).toBe(false)
  })
  it('leaves public shared conversations independent of gateway authentication', () => {
    window.location.hash = '#share/public'
    renderer.update()
    expect(apiFetch).not.toHaveBeenCalled()
    expect(node.querySelector('audio').src).toBe(original)
  })
})
