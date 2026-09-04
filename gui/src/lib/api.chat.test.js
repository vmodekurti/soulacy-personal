import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './api.js'

beforeEach(() => {
  globalThis.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    text: async () => JSON.stringify({ reply: 'ok' }),
  }))
})

describe('api.chat response mode', () => {
  it('sends voice mode as metadata without changing the message', async () => {
    await api.chat('stock-advisor', 'How is Micron?', 'gui-user', null, 'session-1', [], 'voice')

    const [url, options] = fetch.mock.calls[0]
    const body = JSON.parse(options.body)
    expect(url).toBe('/api/v1/chat')
    expect(body.text).toBe('How is Micron?')
    expect(body.response_mode).toBe('voice')
  })

  it('does not add response_mode to ordinary text chat', async () => {
    await api.chat('stock-advisor', 'Hello')
    const body = JSON.parse(fetch.mock.calls[0][1].body)
    expect(body).not.toHaveProperty('response_mode')
  })
})
