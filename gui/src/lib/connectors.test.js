import { describe, expect, it } from 'vitest'
import { advancedCapabilityCount, connectorCategories, connectorExamples, connectorList, connectorPayload, filterConnectors, selectedPlanSites, siteAccessLabel } from './connectors.js'

const items = [
  { id: 'shop', name: 'My stores', intent: 'Compare products', category: 'shopping', sites: [{ name: 'Etsy', domain: 'etsy.com' }] },
  { id: 'event', name: 'Weekend events', intent: 'Find concerts', category: 'events', sites: [{ name: 'Meetup', domain: 'meetup.com' }] },
]

describe('connector composer helpers', () => {
  it('normalizes malformed API payloads', () => {
    expect(connectorList({})).toEqual([])
    expect(connectorList({ connectors: items })).toEqual(items)
    expect(connectorExamples({})).toEqual([])
  })

  it('filters user connectors by category, intent, and website', () => {
    expect(filterConnectors(items, '', 'events').map((item) => item.id)).toEqual(['event'])
    expect(filterConnectors(items, 'etsy', 'all').map((item) => item.id)).toEqual(['shop'])
    expect(filterConnectors(items, 'concert', 'shopping')).toEqual([])
  })

  it('builds a create payload from selected suggestions only', () => {
    const plan = {
      name: 'Shopping connector', intent: 'Compare products', category: 'shopping',
      sites: [
        { id: 'a', name: 'A', base_url: 'https://a.com', selected: true, reason: 'Suggested' },
        { id: 'b', name: 'B', base_url: 'https://b.com', selected: false },
      ],
    }
    expect(selectedPlanSites(plan).map((site) => site.id)).toEqual(['a'])
    expect(connectorPayload(plan)).toEqual({
      name: 'Shopping connector', intent: 'Compare products', category: 'shopping',
      sites: [{ id: 'a', name: 'A', base_url: 'https://a.com' }],
    })
  })

  it('summarizes categories, access, and advanced capabilities', () => {
    expect(connectorCategories(items)).toEqual(['events', 'shopping'])
    expect(siteAccessLabel({})).toBe('Public access ready')
    expect(siteAccessLabel({ auth_connection_id: 'conn_1' })).toBe('Website sign-in connected')
    expect(advancedCapabilityCount({ sites: [{ advanced_capabilities: [{}, {}] }, { advanced_capabilities: [{}] }] })).toBe(3)
  })
})
