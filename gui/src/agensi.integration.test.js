import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { api } from './lib/api.js'

const skillsPage = readFileSync(fileURLToPath(new URL('./pages/Skills.svelte', import.meta.url)), 'utf8')

function jsonResponse(body) {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) }
}

beforeEach(() => {
  globalThis.fetch = vi.fn().mockResolvedValue(jsonResponse({}))
})

describe('Agensi first-class integration', () => {
  it('uses dedicated authenticated status, connect and search APIs', async () => {
    await api.skills.agensiStatus()
    await api.skills.connectAgensi()
    await api.skills.searchAgensi('contract review')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/skills/agensi/status', expect.any(Object))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/skills/agensi/connect', expect.objectContaining({ method: 'POST' }))
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/skills/agensi/search', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ query: 'contract review' }),
    }))
  })

  it('shows sign-in and purchase rules without asking for a token', () => {
    expect(skillsPage).toContain('Connect Agensi')
    expect(skillsPage).toContain('Secure browser sign-in')
    expect(skillsPage).toContain('Latest version loaded each time')
    expect(skillsPage).toContain('Payment always needs your approval')
    expect(skillsPage).toContain('Use with Genie')
    expect(skillsPage).toContain('genieRequest')
    expect(skillsPage).not.toMatch(/Agensi[^\n]{0,40}(API key|token)/i)
  })

  it('uses theme surfaces instead of fixed dark cards', () => {
    expect(skillsPage).toContain('.agensi-card {')
    expect(skillsPage).toContain('background: var(--sl-surface)')
    expect(skillsPage).toContain('background: var(--sl-surface-inset)')
  })
})
