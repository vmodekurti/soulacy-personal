const STALE_ASSET_RELOAD_KEY = 'soulacy:stale-asset-reload'
const STALE_ASSET_RELOAD_COOLDOWN_MS = 15000

export function looksLikeStaleAssetError(value) {
  const msg = String(value?.message || value?.reason?.message || value?.reason || value || '')
  return (
    msg.includes('Failed to fetch dynamically imported module') ||
    msg.includes('Importing a module script failed') ||
    msg.includes('error loading dynamically imported module') ||
    (msg.includes('/assets/') && msg.includes('.js'))
  )
}

export async function recoverFromStaleAssets({ force = false } = {}) {
  const now = Date.now()
  const last = Number(sessionStorage.getItem(STALE_ASSET_RELOAD_KEY) || 0)
  if (!force && last && now - last < STALE_ASSET_RELOAD_COOLDOWN_MS) return false
  sessionStorage.setItem(STALE_ASSET_RELOAD_KEY, String(now))
  try {
    if ('serviceWorker' in navigator) {
      const regs = await navigator.serviceWorker.getRegistrations()
      await Promise.all(regs.map(async (reg) => {
        await reg.update().catch(() => undefined)
        await reg.unregister().catch(() => undefined)
      }))
    }
    if ('caches' in window) {
      const keys = await caches.keys()
      await Promise.all(keys.filter((key) => key.startsWith('soulacy-shell-')).map((key) => caches.delete(key)))
    }
  } catch (_) {}
  const url = new URL(window.location.href)
  url.searchParams.set('__soulacy_reload', String(now))
  window.location.replace(url.toString())
  return true
}

export function clearStaleAssetRecoveryMarker() {
  sessionStorage.removeItem(STALE_ASSET_RELOAD_KEY)
  const url = new URL(window.location.href)
  if (!url.searchParams.has('__soulacy_reload')) return
  url.searchParams.delete('__soulacy_reload')
  window.history.replaceState(window.history.state, '', url.toString())
}
