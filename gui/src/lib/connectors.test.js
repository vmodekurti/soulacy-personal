import { describe, expect, it } from 'vitest'
import { connectorCategories, connectorList, effectSummary, filterConnectors } from './connectors.js'

const items = [
  { id: 'shop', name: 'Shop', provider: 'Vendor', category: 'shopping', summary: 'Find products', capabilities: [{ label: 'Compare prices', effect: 'read' }] },
  { id: 'event', name: 'Events', provider: 'Tickets', category: 'events', summary: 'Find concerts', capabilities: [{ label: 'Reserve', effect: 'write' }] },
]

describe('connector catalog helpers', () => {
  it('normalizes malformed API payloads', () => {
    expect(connectorList({})).toEqual([])
    expect(connectorList({ connectors: items })).toEqual(items)
  })

  it('filters by category and capability text', () => {
    expect(filterConnectors(items, '', 'events').map((item) => item.id)).toEqual(['event'])
    expect(filterConnectors(items, 'compare', 'all').map((item) => item.id)).toEqual(['shop'])
    expect(filterConnectors(items, 'concert', 'shopping')).toEqual([])
  })

  it('derives categories and risk summaries', () => {
    expect(connectorCategories(items)).toEqual(['events', 'shopping'])
    expect(effectSummary(items[0].capabilities)).toBe('Read only')
    expect(effectSummary(items[1].capabilities)).toBe('1 write action')
  })
})
