import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const chat = readFileSync(fileURLToPath(new URL('./Chat.svelte', import.meta.url)), 'utf8')
const api = readFileSync(fileURLToPath(new URL('../lib/api.js', import.meta.url)), 'utf8')

describe('chat response feedback', () => {
  it('renders both response ratings and submits server-issued run identity', () => {
    expect(chat).toContain('Mark this response helpful')
    expect(chat).toContain('Mark this response unhelpful')
    expect(chat).toContain('run_id: runId')
    expect(chat).toContain('response_id: msg.responseId')
    expect(api).toContain("apiFetch('/chat/feedback'")
  })
})
