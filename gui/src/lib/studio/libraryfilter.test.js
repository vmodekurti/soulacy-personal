import { describe, it, expect } from 'vitest'
import {
  partitionLibrary, filterLibrary, libraryFacets, hasActiveFilters, emptyQuery,
} from './libraryfilter.js'

const agents = [
  { id: 'a1', name: 'News digest', enabled: true, trigger: 'schedule', strategy: 'workflow', integrations: ['telegram'], owner: 'vasu' },
  { id: 'a2', name: 'Support triage', enabled: false, trigger: 'channel', strategy: 'react', integrations: ['slack'], owner: 'sam' },
  { id: 'a3', name: 'Nightly backup', enabled: true, trigger: 'schedule', strategy: 'workflow', integrations: ['s3', 'slack'] },
]
const drafts = [
  { id: 'd1', name: 'Half-built thing', updated: '2026-07-24T10:00:00Z' },
]

describe('partitionLibrary', () => {
  it('separates deployed from merely saved — they have opposite operational meaning', () => {
    const p = partitionLibrary(agents, drafts)
    expect(p.deployed.map((x) => x.id)).toEqual(['a1', 'a3'])
    expect(p.saved.map((x) => x.id)).toEqual(['a2'])
    expect(p.drafts.map((x) => x.id)).toEqual(['d1'])
  })

  it('tags each item with its kind so one list can render all three', () => {
    const p = partitionLibrary(agents, drafts)
    expect(p.deployed[0].kind).toBe('deployed')
    expect(p.saved[0].kind).toBe('saved')
    expect(p.drafts[0].kind).toBe('draft')
  })

  it('tolerates missing payloads instead of throwing', () => {
    expect(partitionLibrary(null, undefined)).toEqual({ deployed: [], saved: [], drafts: [] })
  })
})

describe('filterLibrary', () => {
  it('returns everything for an empty query', () => {
    expect(filterLibrary(agents, emptyQuery())).toHaveLength(3)
  })

  it('matches free text across name and description', () => {
    expect(filterLibrary(agents, { text: 'digest' }).map((x) => x.id)).toEqual(['a1'])
  })

  it('requires every term, so typing narrows rather than widens', () => {
    expect(filterLibrary(agents, { text: 'nightly backup' })).toHaveLength(1)
    expect(filterLibrary(agents, { text: 'nightly digest' })).toHaveLength(0)
  })

  it('is case insensitive', () => {
    expect(filterLibrary(agents, { text: 'NEWS' })).toHaveLength(1)
  })

  it('filters by trigger, strategy and owner', () => {
    expect(filterLibrary(agents, { trigger: 'schedule' })).toHaveLength(2)
    expect(filterLibrary(agents, { strategy: 'react' }).map((x) => x.id)).toEqual(['a2'])
    expect(filterLibrary(agents, { owner: 'sam' }).map((x) => x.id)).toEqual(['a2'])
  })

  it('filters by integration across a list', () => {
    expect(filterLibrary(agents, { integration: 'slack' }).map((x) => x.id)).toEqual(['a2', 'a3'])
  })

  it('filters by status using the deployed/saved/draft split', () => {
    const all = [...partitionLibrary(agents, drafts).deployed,
                 ...partitionLibrary(agents, drafts).saved,
                 ...partitionLibrary(agents, drafts).drafts]
    expect(filterLibrary(all, { status: 'deployed' })).toHaveLength(2)
    expect(filterLibrary(all, { status: 'saved' })).toHaveLength(1)
    expect(filterLibrary(all, { status: 'draft' })).toHaveLength(1)
  })

  it('excludes an item that declares a different value for the facet', () => {
    expect(filterLibrary(agents, { trigger: 'webhook' })).toHaveLength(0)
  })

  it('excludes items missing the facet entirely rather than guessing', () => {
    // a3 has no owner; an owner filter must not match it either way.
    expect(filterLibrary(agents, { owner: 'vasu' }).map((x) => x.id)).toEqual(['a1'])
  })

  it('combines text and facets', () => {
    expect(filterLibrary(agents, { text: 'backup', trigger: 'schedule' }).map((x) => x.id)).toEqual(['a3'])
    expect(filterLibrary(agents, { text: 'backup', trigger: 'channel' })).toHaveLength(0)
  })

  it('survives null items and payloads', () => {
    expect(filterLibrary([null, ...agents], { text: 'news' })).toHaveLength(1)
    expect(filterLibrary(null, {})).toEqual([])
  })
})

describe('libraryFacets', () => {
  it('offers only values that are actually present, sorted', () => {
    const f = libraryFacets(agents)
    expect(f.triggers).toEqual(['channel', 'schedule'])
    expect(f.strategies).toEqual(['react', 'workflow'])
    expect(f.integrations).toEqual(['s3', 'slack', 'telegram'])
    expect(f.owners).toEqual(['sam', 'vasu'])
  })

  it('returns empty facet lists rather than undefined', () => {
    expect(libraryFacets([])).toEqual({ triggers: [], strategies: [], integrations: [], owners: [] })
  })
})

describe('hasActiveFilters', () => {
  it('is false for a fresh query and true once anything is set', () => {
    expect(hasActiveFilters(emptyQuery())).toBe(false)
    expect(hasActiveFilters({ ...emptyQuery(), text: '  ' })).toBe(false)
    expect(hasActiveFilters({ ...emptyQuery(), text: 'x' })).toBe(true)
    expect(hasActiveFilters({ ...emptyQuery(), status: 'draft' })).toBe(true)
  })
})
