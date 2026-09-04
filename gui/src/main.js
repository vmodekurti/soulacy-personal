import App from './App.svelte'
import { installPrompt, appInstalled } from './lib/stores.js'
import { clearStaleAssetRecoveryMarker, looksLikeStaleAssetError, recoverFromStaleAssets } from './lib/stalerecovery.js'

window.addEventListener('error', (event) => {
  if (looksLikeStaleAssetError(event.error || event.message)) recoverFromStaleAssets()
})
window.addEventListener('unhandledrejection', (event) => {
  if (looksLikeStaleAssetError(event.reason)) recoverFromStaleAssets()
})

const app = new App({ target: document.getElementById('app') })
clearStaleAssetRecoveryMarker()

if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {})
  })
}

// PWA install prompt: the browser fires beforeinstallprompt when the app is
// installable. Stash the event so the UI can offer an explicit "Install app"
// button (Chrome/Edge/Android) rather than relying on the browser's own hint.
window.addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault()
  installPrompt.set(e)
})
window.addEventListener('appinstalled', () => {
  installPrompt.set(null)
  appInstalled.set(true)
})

export default app
