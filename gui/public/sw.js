const CACHE = 'soulacy-shell-v3'
const SHELL = ['/', '/manifest.webmanifest']

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(SHELL)).catch(() => undefined))
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)))),
  )
  self.clients.claim()
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (url.pathname.startsWith('/api/')) return
  if (url.pathname === '/sw.js' || url.pathname.startsWith('/assets/')) {
    event.respondWith(fetch(req))
    return
  }
  event.respondWith(
    fetch(req).then((res) => {
      if (!res || !res.ok || res.type === 'opaqueredirect') return res
      const copy = res.clone()
      caches.open(CACHE).then((cache) => cache.put(req, copy)).catch(() => undefined)
      return res
    }).catch(() => caches.match(req).then((hit) => hit || caches.match('/'))),
  )
})

// Web Push: render notifications sent by the gateway (e.g. "a tool needs
// approval"). The payload is JSON produced by internal/webpush.Notification.
self.addEventListener('push', (event) => {
  let data = { title: 'Soulacy', body: 'You have a new notification', url: '/#mobile' }
  try { if (event.data) data = Object.assign(data, event.data.json()) } catch (_) {}
  event.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      tag: data.tag || undefined,
      data: { url: data.url || '/#mobile' },
      badge: '/icon.svg',
      icon: '/icon.svg',
    }),
  )
})

// Focus (or open) the app on the target page when a notification is tapped.
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = (event.notification.data && event.notification.data.url) || '/#mobile'
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if ('focus' in client) { client.focus(); client.navigate && client.navigate(target); return }
      }
      if (self.clients.openWindow) return self.clients.openWindow(target)
    }),
  )
})
