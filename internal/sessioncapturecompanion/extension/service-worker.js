const sessions = new Map()
const soulacyPagePatterns = [
  'https://*.soulacy.io/*',
  'http://localhost/*',
  'http://127.0.0.1/*'
]

async function injectBridge(tabId) {
  if (!tabId) return
  try {
    await chrome.scripting.executeScript({ target: { tabId }, files: ['content-script.js'] })
  } catch (_) {
    // Restricted pages and tabs that close during installation are safe to
    // ignore. Normal navigations remain covered by manifest content_scripts.
  }
}

async function connectExistingSoulacyTabs() {
  const tabs = await chrome.tabs.query({ url: soulacyPagePatterns })
  await Promise.all(tabs.map(tab => injectBridge(tab.id)))
}

// Manifest content scripts are not applied retroactively to tabs that were
// already open when an unpacked extension was installed or updated.
chrome.runtime.onInstalled.addListener(() => { void connectExistingSoulacyTabs() })
chrome.runtime.onStartup.addListener(() => { void connectExistingSoulacyTabs() })

function cleanDomain(value) {
  return String(value || '').trim().toLowerCase().replace(/^\*\./, '').replace(/^\./, '').replace(/\.$/, '')
}

function hostAllowed(host, domains) {
  host = cleanDomain(host)
  return domains.some(domain => host === domain || host.endsWith(`.${domain}`))
}

function normalizeBoundary(payload) {
  const base = new URL(payload.baseUrl)
  if (base.protocol !== 'https:') throw new Error('Only HTTPS sign-in URLs are supported.')
  const domains = [...new Set((payload.allowedDomains || []).map(cleanDomain).filter(Boolean))]
  if (!domains.length) domains.push(cleanDomain(base.hostname))
  if (!hostAllowed(base.hostname, domains)) throw new Error('The sign-in URL is outside the approved domain boundary.')
  return { base, domains }
}

function permissionOrigins(domains) {
  return domains.flatMap(domain => [`https://${domain}/*`, `https://*.${domain}/*`])
}

function sameSite(value) {
  if (value === 'no_restriction') return 'None'
  if (value === 'strict') return 'Strict'
  return 'Lax'
}

async function openSession(payload) {
  const { base, domains } = normalizeBoundary(payload)
  const granted = await chrome.permissions.request({ origins: permissionOrigins(domains) })
  if (!granted) throw new Error('Website permission was not approved. Soulacy requests access only to the approved domain.')
  const tab = await chrome.tabs.create({ url: base.href, active: true })
  sessions.set(String(payload.connectionId), { tabId: tab.id, domains })
  await chrome.storage.session.set({ [`connection:${payload.connectionId}`]: { tabId: tab.id, domains } })
  return { tabId: tab.id, domains }
}

async function loadSession(connectionId) {
  const key = String(connectionId)
  if (sessions.has(key)) return sessions.get(key)
  const stored = await chrome.storage.session.get(`connection:${key}`)
  return stored[`connection:${key}`]
}

async function captureSession(payload) {
  const { domains } = normalizeBoundary(payload)
  const session = await loadSession(payload.connectionId)
  if (!session?.tabId) throw new Error('The secure sign-in tab is no longer open. Select Open secure sign-in again.')
  const tab = await chrome.tabs.get(session.tabId)
  const current = new URL(tab.url || '')
  if (!hostAllowed(current.hostname, domains)) throw new Error('Return the sign-in tab to the approved website before saving the session.')

  const cookieSets = await Promise.all(domains.map(domain => chrome.cookies.getAll({ domain })))
  const cookies = cookieSets.flat().filter(cookie => hostAllowed(cookie.domain, domains)).map(cookie => ({
    name: cookie.name,
    value: cookie.value,
    domain: cookie.domain,
    path: cookie.path || '/',
    expires: typeof cookie.expirationDate === 'number' ? cookie.expirationDate : -1,
    httpOnly: Boolean(cookie.httpOnly),
    secure: Boolean(cookie.secure),
    sameSite: sameSite(cookie.sameSite)
  }))

  const injected = await chrome.scripting.executeScript({
    target: { tabId: session.tabId },
    func: () => ({ origin: location.origin, localStorage: Object.entries(localStorage).map(([name, value]) => ({ name, value })) })
  })
  const origin = injected?.[0]?.result
  const origins = origin && hostAllowed(new URL(origin.origin).hostname, domains) && origin.localStorage.length ? [origin] : []
  if (!cookies.length && !origins.length) throw new Error('No signed-in state was found. Complete the website login, then try Save session again.')
  return { storageState: { cookies, origins } }
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  ;(async () => {
    if (message?.action === 'ping') return { ok: true, payload: { version: chrome.runtime.getManifest().version } }
    if (message?.action === 'open') return { ok: true, payload: await openSession(message.payload || {}) }
    if (message?.action === 'capture') return { ok: true, payload: await captureSession(message.payload || {}) }
    return { ok: false, error: 'Unsupported companion action.' }
  })().then(sendResponse).catch(error => sendResponse({ ok: false, error: error?.message || String(error) }))
  return true
})
