// Plugin GUI mount helpers (Story E8). Pure functions — unit-tested in
// pluginui.test.js. The shell lists mounts from /api/v1/plugins/ui, renders
// nav entries, and embeds each plugin UI in a sandboxed iframe that receives
// a SCOPED plugin token (never the user's API key).

const PAGE_PREFIX = 'plugin:'

/** Nav entries for the sidebar from the /plugins/ui mounts payload. */
export function pluginNavEntries(mounts) {
  if (!Array.isArray(mounts)) return []
  return mounts
    .filter((m) => m && m.id && m.url)
    .map((m) => ({
      id: PAGE_PREFIX + m.id,
      icon: m.icon || '🧩',
      label: m.label || m.id,
      group: 'plugins',
      url: m.url,
    }))
}

/** True when a page id addresses a plugin UI. */
export function isPluginPage(pageId) {
  return typeof pageId === 'string' && pageId.startsWith(PAGE_PREFIX) && pageId.length > PAGE_PREFIX.length
}

/** Extract the plugin id from a plugin page id ('' when not a plugin page). */
export function pluginIdFromPage(pageId) {
  return isPluginPage(pageId) ? pageId.slice(PAGE_PREFIX.length) : ''
}

/**
 * Build the iframe src: mount URL + scoped token in the fragment.
 * The fragment (not a query param) keeps the token out of server access
 * logs; the plugin reads it via location.hash.
 */
export function iframeSrc(url, token) {
  if (!url) return ''
  if (!token) return url
  return url + '#token=' + encodeURIComponent(token)
}

/**
 * Sandbox attribute for plugin iframes: scripts + forms only.
 * No same-origin (the plugin must use its token, not the user's cookies),
 * no top-navigation, no popups.
 */
export const IFRAME_SANDBOX = 'allow-scripts allow-forms'
