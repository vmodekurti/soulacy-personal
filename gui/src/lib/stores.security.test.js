// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'

describe('credential storage', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.resetModules()
  })

  it('migrates a legacy API key into tab-scoped session storage', async () => {
    localStorage.setItem('soulacy_api_key', 'legacy-secret')
    const { apiKey } = await import('./stores.js')
    expect(get(apiKey)).toBe('legacy-secret')
    expect(localStorage.getItem('soulacy_api_key')).toBeNull()
    expect(sessionStorage.getItem('soulacy_api_key')).toBe('legacy-secret')
  })
})
