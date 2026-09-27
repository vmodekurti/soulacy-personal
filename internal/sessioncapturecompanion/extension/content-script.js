(() => {
  if (window.__soulacySessionCaptureBridge) return
  window.__soulacySessionCaptureBridge = true

  const WEB_SOURCE = 'soulacy-web'
  const EXT_SOURCE = 'soulacy-session-capture'
  window.addEventListener('message', async event => {
    if (event.source !== window || event.origin !== location.origin) return
    const message = event.data
    if (!message || message.source !== WEB_SOURCE || !message.requestId) return
    if (!['ping', 'open', 'capture'].includes(message.action)) return
    try {
      const response = await chrome.runtime.sendMessage({ action: message.action, payload: message.payload || {} })
      window.postMessage({ source: EXT_SOURCE, requestId: message.requestId, ...response }, location.origin)
    } catch (error) {
      window.postMessage({ source: EXT_SOURCE, requestId: message.requestId, ok: false, error: error?.message || String(error) }, location.origin)
    }
  })
})()
