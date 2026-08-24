// resourceversions.js — the client half of MU-028's optimistic concurrency.
//
// THE FAILURE. Two members open the same agent. One saves. The other saves
// thirty seconds later from a form rendered before that, and the first
// member's change is gone — no error, no conflict, nothing in the UI that
// looks wrong. The only evidence is that work disappeared, usually noticed
// days later by whoever did it.
//
// WHY A REGISTRY RATHER THAN A PROP. The version could be threaded through
// every page that edits an agent — Agents, Schedule, Flow, Studio — and each
// of those would then be one refactor away from dropping it. A caller that
// forgets to send a precondition is exactly the caller that overwrites
// silently, which is the bug. So the version is captured and replayed by the
// transport: a page opts out by sending its own If-Match, never by forgetting.
//
// The registry is per tab and deliberately not persisted. A version that
// outlived a reload would be a claim about server state made by a client that
// has not looked since — which is the stale write wearing a cache.

// versions maps a resource key to the last validator the server handed us.
const versions = new Map()

// resourceKey reduces a request path to the thing being versioned.
//
// `/agents/support-bot`, `/agents/support-bot/yaml` and
// `/agents/support-bot/rollback` are three ways to write ONE agent, and the
// server derives one validator for all of them from the definition itself. A
// key per path would let a save through the code view carry a version obtained
// from the form view, which is a different string for the same content — and
// then every cross-view save is a false conflict.
//
// Returns '' for anything that is not a versioned resource, which is how the
// transport decides to stay out of the way.
export function resourceKey(path) {
  if (typeof path !== 'string') return ''
  const clean = path.split('?')[0].replace(/^\/+/, '')
  const parts = clean.split('/').filter(Boolean)
  // Studio re-opens the same agent through its draft projection. It is not a
  // different resource: /studio/agents/:id and /agents/:id are two views over
  // the same definition and therefore share one validator.
  if (parts[0] === 'studio' && parts[1] === 'agents' && parts.length === 3) {
    return `agents/${parts[2]}`
  }
  if (parts[0] !== 'agents' || parts.length < 2) return ''
  // `/agents/validate` and `/agents/package/...` are not an agent's identity.
  if (parts[1] === 'validate' || parts[1] === 'package') return ''
  return `agents/${parts[1]}`
}

export function rememberVersion(path, etag) {
  const key = resourceKey(path)
  if (!key || !etag) return
  versions.set(key, etag)
}

// rememberVersionsFor records the map GET /agents returns, so an edit started
// from the list carries a precondition. Nothing in the GUI ever fetched a
// single agent, so without this there was no version to send at all.
export function rememberVersions(map) {
  if (!map || typeof map !== 'object') return
  for (const [id, etag] of Object.entries(map)) {
    if (id && etag) versions.set(`agents/${id}`, etag)
  }
}

export function versionFor(path) {
  const key = resourceKey(path)
  return key ? versions.get(key) || '' : ''
}

// forgetVersion drops a stale token. Called when the server tells us ours is
// wrong and we have nothing better: keeping it would make the next save fail
// the same way for the same reason.
export function forgetVersion(path) {
  const key = resourceKey(path)
  if (key) versions.delete(key)
}

export function clearVersions() {
  versions.clear()
}

// STALE_WRITE is the server's code for both halves of the mechanism: 409 when
// the version is out of date, 428 when none was sent. They are separate
// statuses because the remedies differ — "reload and reconcile" is useless
// advice to a client that is not participating in versioning at all.
export const STALE_WRITE = 'stale_resource_version'

// isConflict reports a save that lost a race. A caller acts on this by
// offering reload / compare / overwrite, never by retrying silently: an
// automatic retry with the server's current version is precisely the silent
// overwrite the whole mechanism exists to prevent.
export function isConflict(err) {
  return !!err && err.status === 409 && err.body?.code === STALE_WRITE
}

// isPreconditionRequired reports that this client sent no version. Recoverable
// without asking anybody: reload the resource and try again.
export function isPreconditionRequired(err) {
  return !!err && err.status === 428 && err.body?.code === STALE_WRITE
}

// conflictDetail normalises what a 409 carries into what a dialog renders.
export function conflictDetail(err) {
  const body = err?.body || {}
  return {
    message: body.error || 'This was changed since you loaded it.',
    currentVersion: body.current_version || body.etag || '',
    suppliedVersion: body.supplied_version || '',
    lastModifiedBy: body.last_modified_by || '',
    lastModifiedAt: body.last_modified_at || '',
    remedy: body.remedy || '',
  }
}
