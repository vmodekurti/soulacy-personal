import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiKey } from './stores.js'
import { activeWorkspace } from './workspace.js'
import { createEventSocket } from './api.js'

describe('event socket workspace scope', () => {
  beforeEach(() => {
    apiKey.set('')
    activeWorkspace.set(null)
    globalThis.WebSocket = vi.fn(function (url) { this.url = url })
  })

  it('carries the selected workspace when browser headers are unavailable', () => {
    activeWorkspace.set({ workspaceId: 'ws_selected' })
    createEventSocket()
    const url = new URL(WebSocket.mock.calls[0][0])
    expect(url.pathname).toBe('/ws/events')
    expect(url.searchParams.get('workspace_id')).toBe('ws_selected')
  })

  it('preserves API-key authentication alongside workspace selection', () => {
    apiKey.set('test-credential')
    activeWorkspace.set({ workspaceId: 'ws_selected' })
    createEventSocket()
    const url = new URL(WebSocket.mock.calls[0][0])
    expect(url.searchParams.get('api_key')).toBe('test-credential')
    expect(url.searchParams.get('workspace_id')).toBe('ws_selected')
  })
})
