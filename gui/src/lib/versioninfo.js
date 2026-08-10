// What version am I running?
//
// The answer existed in the UI only inside the "Update Available" banner:
//
//   {#if updateInfo && updateInfo.update_available}
//     Soulacy {latest} is available! (Current: {current})
//   {/if}
//
// So the version was visible exactly when you were behind, and invisible the
// rest of the time — which is most of the time, and is precisely when someone
// asks "what am I on?": before reporting a bug, after an upgrade, when checking
// whether a release landed. The only ways left were `soulacy --version`, a curl
// at /health, or grepping the startup log.
//
// Pure on purpose: the interesting part is what to SAY for each shape of the
// updates payload, and that should be testable without mounting Config.svelte.

/**
 * @param {object|null} info  the /system/updates/status payload
 * @returns {{version: string, status: 'current'|'behind'|'checking'|'unknown', detail: string}}
 */
export function versionSummary(info) {
  if (!info) {
    return {
      version: 'unknown',
      status: 'unknown',
      // Do not say "you are up to date" when we simply could not ask. A wrong
      // reassurance here is worse than an admission.
      detail: 'Could not reach the update service — the version shown by `soulacy --version` is authoritative.',
    }
  }

  const version = String(info.current_version || '').trim() || 'unknown'

  if (info.checking) {
    return { version, status: 'checking', detail: 'Checking for updates…' }
  }

  if (info.update_available) {
    const latest = String(info.latest_version || '').trim()
    return {
      version,
      status: 'behind',
      detail: latest ? `${latest} is available.` : 'A newer version is available.',
    }
  }

  return { version, status: 'current', detail: 'Up to date.' }
}

/** "checked 4 minutes ago" — vague on purpose; the exact second is noise. */
export function lastCheckedLabel(iso, now = new Date()) {
  if (!iso) return ''
  const then = new Date(iso)
  if (Number.isNaN(then.getTime())) return ''
  const mins = Math.floor((now.getTime() - then.getTime()) / 60000)
  if (mins < 0) return ''
  if (mins < 1) return 'checked just now'
  if (mins < 60) return `checked ${mins} minute${mins === 1 ? '' : 's'} ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `checked ${hrs} hour${hrs === 1 ? '' : 's'} ago`
  const days = Math.floor(hrs / 24)
  return `checked ${days} day${days === 1 ? '' : 's'} ago`
}
