// Which build of the runtime is this portal talking to, and did it just change?
//
// The gateway reports `build` on /health (internal/buildinfo): a codename
// label derived from a hash of the running binary, a per-process instance id,
// and the usual version/commit details. A new label means new code is live; a
// new instance with the same label means the runtime only restarted.
//
// The comparison logic is pure so it can be tested without mounting the shell;
// startBuildWatch wires it to a probe, a timer and browser storage.

import { writable } from 'svelte/store'

/** The build the portal last heard from the gateway, or null before the first answer. */
export const runtimeBuild = writable(null)

/**
 * The most recent change worth telling the operator about, or null.
 * kind: 'updated' (different build) | 'restarted' (same build, new process)
 * reloadNeeded: true when this page was loaded from the previous build, so its
 * interface code is older than the runtime it is now talking to.
 */
export const buildChange = writable(null)

export const LAST_SEEN_KEY = 'soulacy.lastSeenBuild'
export const FRESH_WINDOW_MS = 15 * 60 * 1000

/** Normalise a /health payload into a build record, or null when absent. */
export function buildFromHealth(health) {
  const b = health && health.build
  if (!b || typeof b !== 'object' || !b.label || !b.build_id) return null
  return {
    label: String(b.label),
    build_id: String(b.build_id),
    version: String(b.version || ''),
    commit: String(b.commit || ''),
    modified: !!b.modified,
    built_at: b.built_at || '',
    started_at: b.started_at || '',
    instance_id: String(b.instance_id || ''),
    go_version: String(b.go_version || ''),
  }
}

/** 'none' | 'restarted' | 'updated' — how `next` differs from `prev`. */
export function classifyChange(prev, next) {
  if (!prev || !next) return 'none'
  if (prev.build_id !== next.build_id) return 'updated'
  if (prev.instance_id && next.instance_id && prev.instance_id !== next.instance_id) return 'restarted'
  return 'none'
}

/** True while a build started within the fresh window — the "just deployed" cue. */
export function isFreshStart(build, now = Date.now(), windowMs = FRESH_WINDOW_MS) {
  if (!build || !build.started_at) return false
  const started = new Date(build.started_at).getTime()
  if (Number.isNaN(started)) return false
  const age = now - started
  return age >= 0 && age < windowMs
}

/** "just now", "5 min ago", "3 h ago", "2 days ago". */
export function agoLabel(iso, now = Date.now()) {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const mins = Math.floor((now - then) / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins} min ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 48) return `${hrs} h ago`
  return `${Math.floor(hrs / 24)} days ago`
}

/** Short commit with a dirty marker, e.g. "fc2fe43 + local changes". */
export function commitLabel(build) {
  if (!build || !build.commit) return 'not recorded'
  return build.commit.slice(0, 7) + (build.modified ? ' + local changes' : '')
}

/** Plain-text block for the "Copy details" button and bug reports. */
export function buildDetailsText(build, now = Date.now()) {
  if (!build) return 'Soulacy runtime build: unknown'
  const lines = [
    `Soulacy runtime build: ${build.label} (${build.build_id})`,
    `Version: ${build.version || 'unknown'}`,
    `Commit: ${commitLabel(build)}`,
  ]
  if (build.built_at) lines.push(`Built: ${build.built_at} (${agoLabel(build.built_at, now)})`)
  if (build.started_at) lines.push(`Started: ${build.started_at} (${agoLabel(build.started_at, now)})`)
  if (build.instance_id) lines.push(`Instance: ${build.instance_id}`)
  return lines.join('\n')
}

function readStored(storage) {
  try {
    const raw = storage && storage.getItem(LAST_SEEN_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

function writeStored(storage, build) {
  try {
    storage && storage.setItem(LAST_SEEN_KEY, JSON.stringify(build))
  } catch {
    // Private windows and blocked storage: the in-page comparison still works.
  }
}

/**
 * Create a watcher. `observe(health)` feeds one /health answer through the
 * comparison; exposed separately so tests drive it without timers.
 */
export function createBuildWatch({ storage = null, onBuild = () => {}, onChange = () => {} } = {}) {
  let current = null
  let first = true
  return {
    observe(health) {
      const build = buildFromHealth(health)
      if (!build) return
      if (first) {
        first = false
        // The page just loaded. If the browser last saw a different build,
        // this load already carries the new interface: announce, no reload.
        const previous = readStored(storage)
        if (previous && classifyChange(previous, build) === 'updated') {
          onChange({ kind: 'updated', from: previous, to: build, reloadNeeded: false })
        }
      } else {
        const kind = classifyChange(current, build)
        if (kind !== 'none') onChange({ kind, from: current, to: build, reloadNeeded: kind === 'updated' })
      }
      current = build
      onBuild(build)
      writeStored(storage, build)
    },
  }
}

let started = false

/**
 * Start the shared watcher once per page. Polls every `intervalMs`, and right
 * away whenever one of `triggers` (stores such as `connected`, or "logged in")
 * turns true: a reconnect is when a redeploy usually becomes visible, and a
 * login is when /health first answers this page.
 */
export function startBuildWatch({ probe, triggers = [], intervalMs = 20000, storage = safeLocalStorage() } = {}) {
  if (started || typeof probe !== 'function') return
  started = true
  const watch = createBuildWatch({
    storage,
    onBuild: b => runtimeBuild.set(b),
    onChange: c => buildChange.set(c),
  })
  const poll = () => probe().then(h => watch.observe(h)).catch(() => {})
  poll()
  setInterval(poll, intervalMs)
  for (const trigger of triggers) {
    if (!trigger || typeof trigger.subscribe !== 'function') continue
    let wasOn = null
    trigger.subscribe(on => {
      if (on && wasOn === false) poll()
      wasOn = !!on
    })
  }
}

function safeLocalStorage() {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}
