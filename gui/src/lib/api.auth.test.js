// Story 1 — auth-error display behavior.
//
// The sidebar and Dashboard render "Authentication required" (instead of
// "Gateway Offline") based on the `authRequired` store. These tests pin the
// store transitions driven by apiFetch.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { get } from 'svelte/store'
import { apiKey, authRequired } from './stores.js'
import { api, apiFetch } from './api.js'

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
  apiKey.set('')
  globalThis.fetch = vi.fn()
})

describe('apiFetch auth-state transitions', () => {
  it('sets authRequired on 401', async () => {
    fetch.mockResolvedValue(jsonResponse(401, { error: 'invalid or missing API key' }))
    await expect(apiFetch('/agents')).rejects.toMatchObject({ status: 401 })
    expect(get(authRequired)).toBe(true)
  })

  it('does not turn an authorization refusal into a login prompt', async () => {
    fetch.mockResolvedValue(jsonResponse(403, { error: 'forbidden' }))
    await expect(apiFetch('/config')).rejects.toMatchObject({ status: 403 })
    expect(get(authRequired)).toBe(false)
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

  it('exchanges the deployment key for a browser session', async () => {
    fetch.mockResolvedValue(jsonResponse(200, { access_token: 'short-lived-access' }))
    await expect(api.auth.login('deployment-secret')).resolves.toEqual({
      accessToken: 'short-lived-access', persistent: true,
    })
    expect(fetch).toHaveBeenCalledWith('/api/v1/auth/token', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ api_key: 'deployment-secret' }),
    }))
  })

  it('refreshes an expired browser session and retries the request', async () => {
    fetch
      .mockResolvedValueOnce(jsonResponse(401, { error: 'expired' }))
      .mockResolvedValueOnce(jsonResponse(200, { access_token: 'renewed-access' }))
      .mockResolvedValueOnce(jsonResponse(200, { agents: [] }))

    await expect(apiFetch('/agents')).resolves.toEqual({ agents: [] })
    expect(get(apiKey)).toBe('renewed-access')
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/auth/refresh', expect.objectContaining({ body: '{}' }))
    expect(get(authRequired)).toBe(false)
  })
})
