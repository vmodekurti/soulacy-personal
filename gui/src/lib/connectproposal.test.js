import { describe, expect, it } from 'vitest'
import { connectProposal, isConnectRequest, siteLabel } from './connectproposal.js'

describe('connect proposals', () => {
  it('only treats connector creation as a connect proposal', () => {
    expect(isConnectRequest({ tool: 'create_connector' })).toBe(true)
    expect(isConnectRequest({ tool: 'portal_confirm' })).toBe(false)
    expect(isConnectRequest({ tool: 'shell_exec' })).toBe(false)
    expect(isConnectRequest(null)).toBe(false)
    expect(connectProposal({ tool: 'shell_exec', args: {} })).toBeNull()
  })

  it('shows a site as its plain domain', () => {
    expect(siteLabel('https://www.Notion.so/product?x=1')).toBe('notion.so')
    expect(siteLabel('news.ycombinator.com')).toBe('news.ycombinator.com')
    expect(siteLabel('  Hacker News ')).toBe('Hacker News')
    expect(siteLabel('')).toBe('')
  })

  it('words one site', () => {
    expect(connectProposal({ tool: 'create_connector', call_id: 'c1', args: { intent: 'Follow launches', sites: ['https://www.producthunt.com'] } })).toEqual({
      callId: 'c1',
      title: 'Connect producthunt.com',
      text: 'Read public pages on producthunt.com.',
      note: 'No sign-in is used and nothing is changed there.',
      intent: 'Follow launches',
      sites: ['producthunt.com'],
    })
  })

  it('uses the given name and lists several sites without repeats', () => {
    const p = connectProposal({ tool: 'create_connector', args: { name: 'Tech news', sites: ['techcrunch.com', 'https://techcrunch.com/latest', 'theverge.com', 'arstechnica.com', 'wired.com', 'engadget.com'] } })
    expect(p.title).toBe('Connect Tech news')
    expect(p.sites).toHaveLength(5)
    expect(p.text).toBe('Read public pages on techcrunch.com, theverge.com, arstechnica.com, and 2 more.')
  })

  it('does not invent sites when none were given', () => {
    const p = connectProposal({ tool: 'create_connector', args: { intent: 'Track flights' } })
    expect(p.title).toBe('Connect these sites')
    expect(p.text).toBe('Read public pages on the sites it suggested.')
    expect(p.sites).toEqual([])
  })
})
