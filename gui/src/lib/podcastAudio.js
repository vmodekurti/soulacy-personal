import { apiFetch } from './api.js'

// Keep stored messages and their phone-facing URLs intact. Only mounted web
// players use a short-lived, same-origin playback capability.
export function podcastAudioRenderer(node) {
  const mounted = new WeakSet()
  const abort = new AbortController()
  let active = true
  function update() {
    node.querySelectorAll('audio[data-podcast-url]').forEach(audio => {
      if (mounted.has(audio)) return
      mounted.add(audio)
      const original = audio.dataset.podcastUrl
      if (window.location.hash.startsWith('#share/')) {
        audio.src = original
        return
      }
      const card = audio.closest('.md-audio-card')
      const link = card?.querySelector('.md-audio-link')
      const status = document.createElement('span')
      status.setAttribute('role', 'status')
      status.className = 'md-audio-status'
      card?.appendChild(status)
      let expires = 0
      let pending
      const present = () => active && node.contains(audio)
      const prepare = () => {
        if (pending) return pending
        pending = (async () => {
          try {
            const result = await apiFetch('/media/podcast/ticket', {
              method: 'POST', body: JSON.stringify({ url: original }), signal: abort.signal,
            })
            if (!present()) return false
            audio.src = result.url
            expires = result.expires_at * 1000
            if (link) link.href = result.url
          } catch (err) {
            if (!present()) return false
            if (err.status !== 404) {
              status.textContent = 'Audio could not load. Select the episode link to retry.'
              return false
            }
            // Other podcast services and shared chats retain their original URL.
            audio.src = original
            expires = Infinity
          }
          status.textContent = ''
          const position = audio.currentTime
          if (position > 0) audio.addEventListener('loadedmetadata', () => {
            audio.currentTime = position
          }, { once: true, signal: abort.signal })
          audio.load()
          return true
        })().finally(() => { pending = null })
        return pending
      }
      link?.addEventListener('click', async event => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
        event.preventDefault()
        if (expires <= Date.now() + 60000 && !await prepare()) return
        if (present()) {
          try { await audio.play() }
          catch { status.textContent = 'Select Play on the audio player to listen.' }
        }
      }, { signal: abort.signal })
      audio.addEventListener('error', () => {
        expires = 0
        status.textContent = 'Audio could not load. Select the episode link to retry.'
      }, { signal: abort.signal })
      void prepare()
    })
  }
  return {
    update,
    destroy() { active = false; abort.abort() },
  }
}
