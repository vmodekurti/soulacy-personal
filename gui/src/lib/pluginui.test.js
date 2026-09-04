import { describe, it, expect } from 'vitest'
import {
  pluginNavEntries, isPluginPage, pluginIdFromPage, iframeSrc, IFRAME_SANDBOX,
} from './pluginui.js'

describe('pluginNavEntries', () => {
  it('maps mounts to nav entries in the plugins group', () => {
    const entries = pluginNavEntries([
      { id: 'matrix-suite', label: 'Matrix', icon: '💬', url: '/plugins/matrix-suite/ui/' },
    ])
    expect(entries).toEqual([{
      id: 'plugin:matrix-suite', icon: '💬', label: 'Matrix',
      group: 'plugins', url: '/plugins/matrix-suite/ui/',
    }])
  })

  it('defaults icon and label, skips malformed mounts', () => {
    const entries = pluginNavEntries([
      { id: 'p1', url: '/plugins/p1/ui/' },
      { label: 'no id' },
      null,
    ])
    expect(entries).toHaveLength(1)
    expect(entries[0].icon).toBe('🧩')
    expect(entries[0].label).toBe('p1')
  })

  it('returns [] for non-arrays', () => {
    expect(pluginNavEntries(undefined)).toEqual([])
    expect(pluginNavEntries(null)).toEqual([])
  })
})

describe('plugin page ids', () => {
  it('round-trips plugin ids', () => {
    expect(isPluginPage('plugin:matrix-suite')).toBe(true)
    expect(pluginIdFromPage('plugin:matrix-suite')).toBe('matrix-suite')
  })

  it('rejects non-plugin pages', () => {
    expect(isPluginPage('dashboard')).toBe(false)
    expect(isPluginPage('plugin:')).toBe(false)
    expect(pluginIdFromPage('dashboard')).toBe('')
  })
})

describe('iframeSrc', () => {
  it('carries the token in the fragment, URL-encoded', () => {
    expect(iframeSrc('/plugins/p/ui/', 'splg_ab/c'))
      .toBe('/plugins/p/ui/#token=splg_ab%2Fc')
  })

  it('omits the fragment without a token', () => {
    expect(iframeSrc('/plugins/p/ui/', '')).toBe('/plugins/p/ui/')
  })

  it('returns empty for missing url', () => {
    expect(iframeSrc('', 'tok')).toBe('')
  })
})

describe('IFRAME_SANDBOX', () => {
  it('never grants same-origin or top navigation', () => {
    expect(IFRAME_SANDBOX).toContain('allow-scripts')
    expect(IFRAME_SANDBOX).not.toContain('allow-same-origin')
    expect(IFRAME_SANDBOX).not.toContain('allow-top-navigation')
  })
})
