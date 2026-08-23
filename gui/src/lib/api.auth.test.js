// Story 1 — auth-error display behavior.
//
// The application routes an expired workspace session back through workspace
// login based on the `authRequired` store. A 403 is only a role denial and must
// not turn into the legacy server-API-key prompt.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { get } from 'svelte/store'
import { authRequired } from './stores.js'
import { apiFetch } from './api.js'

function jsonResponse(status, body = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `HTTP ${status}`,
    json: async () => body,
    text: async () => JSON.stringify(body),
  }
}

beforeEach(() => {
  authRequired.set(false)
  globalThis.fetch = vi.fn()
})

describe('apiFetch auth-state transitions', () => {
  it('sets authRequired on 401', async () => {
    fetch.mockResolvedValue(jsonResponse(401, { error: 'invalid or missing API key' }))
    await expect(apiFetch('/agents')).rejects.toMatchObject({ status: 401 })
    expect(get(authRequired)).toBe(true)
  })

  it('keeps an authenticated viewer signed in when an owner-only call returns 403', async () => {
    fetch.mockResolvedValue(jsonResponse(403, { error: 'forbidden' }))
    await expect(apiFetch('/config')).rejects.toMatchObject({ status: 403 })
    expect(get(authRequired)).toBe(false)
  })

  it('does not clear an existing login prompt on 403', async () => {
    authRequired.set(true)
    fetch.mockResolvedValue(jsonResponse(403, { error: 'forbidden' }))
    await expect(apiFetch('/config')).rejects.toMatchObject({ status: 403 })
    expect(get(authRequired)).toBe(true)
  })

  it('clears authRequired when an authenticated call succeeds', async () => {
    authRequired.set(true)
    fetch.mockResolvedValue(jsonResponse(200, { agents: [] }))
    await apiFetch('/agents')
    expect(get(authRequired)).toBe(false)
  })

  it('does NOT clear authRequired on /health success (health bypasses auth)', async () => {
    authRequired.set(true)
    fetch.mockResolvedValue(jsonResponse(200, { status: 'ok' }))
    await apiFetch('/health')
    expect(get(authRequired)).toBe(true)
  })

  it('leaves authRequired untouched on non-auth errors (500)', async () => {
    fetch.mockResolvedValue(jsonResponse(500, { error: 'boom' }))
    await expect(apiFetch('/agents')).rejects.toMatchObject({ status: 500 })
    expect(get(authRequired)).toBe(false)

    authRequired.set(true)
    await expect(apiFetch('/agents')).rejects.toMatchObject({ status: 500 })
    expect(get(authRequired)).toBe(true)
  })

  it('error carries the server message for the banner', async () => {
    fetch.mockResolvedValue(jsonResponse(401, { error: 'invalid or missing API key' }))
    await expect(apiFetch('/agents')).rejects.toThrow('invalid or missing API key')
  })
})
