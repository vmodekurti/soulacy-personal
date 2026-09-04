// libraryfilter.js — searching and partitioning for "My Workflows" (ST-15).
//
// The library was a two-section modal (Saved agents / Drafts) with no search at
// all, which stops scaling at about a dozen workflows — exactly the point where
// a library becomes worth having.
//
// Two things are fixed here:
//
//  1. THREE states, not two. "Saved agents" conflated a workflow that is
//     deployed and running on its schedule with one that is saved and inert.
//     Those have opposite operational meaning — one is live and one is not —
//     and the only signal distinguishing them was a small badge.
//
//  2. Faceted search over trigger / strategy / integration / status / owner.
//
// Filtering is deliberately tolerant of missing fields: the list endpoints do
// not return every facet for every item, and an item that simply does not
// declare a strategy must not silently vanish from an unrelated search. A
// facet filter only ever excludes items that declare a DIFFERENT value.

/** Case-folded haystack for free-text search across an item's readable fields. */
function haystack(item) {
  const parts = [
    item.name, item.id, item.description, item.trigger,
    item.strategy, item.owner, item.status,
    ...(Array.isArray(item.integrations) ? item.integrations : []),
    ...(Array.isArray(item.tags) ? item.tags : []),
  ]
  return parts.filter(Boolean).join(' ').toLowerCase()
}

/**
 * partitionLibrary splits the raw endpoint payloads into the three states the
 * story asks for. `enabled` is what makes an agent actually run, so it is the
 * deployed/saved boundary.
 */
export function partitionLibrary(agents = [], drafts = []) {
  const list = Array.isArray(agents) ? agents : []
  return {
    deployed: list.filter((a) => a && a.enabled).map((a) => ({ ...a, kind: 'deployed' })),
    saved: list.filter((a) => a && !a.enabled).map((a) => ({ ...a, kind: 'saved' })),
    drafts: (Array.isArray(drafts) ? drafts : []).map((d) => ({ ...d, kind: 'draft' })),
  }
}

/** True when the item declares this facet value, or declares nothing at all. */
function facetOk(declared, wanted) {
  if (!wanted) return true
  if (declared === undefined || declared === null || declared === '') return false
  return String(declared).toLowerCase() === String(wanted).toLowerCase()
}

/**
 * filterLibrary applies a free-text query plus facet filters.
 * `query` = { text, trigger, strategy, integration, status, owner }
 */
export function filterLibrary(items = [], query = {}) {
  const text = (query.text || '').trim().toLowerCase()
  const terms = text ? text.split(/\s+/) : []

  return (Array.isArray(items) ? items : []).filter((item) => {
    if (!item) return false

    if (terms.length) {
      const hay = haystack(item)
      // Every term must appear — narrowing as you type is the expected behaviour.
      if (!terms.every((t) => hay.includes(t))) return false
    }

    if (!facetOk(item.trigger, query.trigger)) return false
    if (!facetOk(item.strategy, query.strategy)) return false
    if (!facetOk(item.owner, query.owner)) return false

    if (query.status) {
      const status = item.kind === 'draft' ? 'draft' : (item.enabled ? 'deployed' : 'saved')
      if (status !== query.status) return false
    }

    if (query.integration) {
      const list = Array.isArray(item.integrations) ? item.integrations : []
      const wanted = String(query.integration).toLowerCase()
      if (!list.some((i) => String(i).toLowerCase() === wanted)) return false
    }

    return true
  })
}

/**
 * libraryFacets collects the values actually present, so the filter dropdowns
 * only ever offer choices that can return something. Offering a facet value
 * that matches nothing is a dead end the user has to discover by trying it.
 */
export function libraryFacets(items = []) {
  const triggers = new Set()
  const strategies = new Set()
  const integrations = new Set()
  const owners = new Set()
  for (const item of Array.isArray(items) ? items : []) {
    if (!item) continue
    if (item.trigger) triggers.add(String(item.trigger))
    if (item.strategy) strategies.add(String(item.strategy))
    if (item.owner) owners.add(String(item.owner))
    for (const i of Array.isArray(item.integrations) ? item.integrations : []) {
      if (i) integrations.add(String(i))
    }
  }
  const sorted = (s) => [...s].sort((a, b) => a.localeCompare(b))
  return {
    triggers: sorted(triggers),
    strategies: sorted(strategies),
    integrations: sorted(integrations),
    owners: sorted(owners),
  }
}

/** True when any filter is active — drives the "clear filters" affordance. */
export function hasActiveFilters(query = {}) {
  return !!(
    (query.text || '').trim() ||
    query.trigger || query.strategy || query.integration || query.status || query.owner
  )
}

/** A fresh, empty query object. */
export function emptyQuery() {
  return { text: '', trigger: '', strategy: '', integration: '', status: '', owner: '' }
}
