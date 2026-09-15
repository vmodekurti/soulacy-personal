import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('../App.svelte', import.meta.url)), 'utf8')

describe('Ask Genie placement', () => {
  it('is mounted by the app shell for signed-in pages, but not on Chat or onboarding', () => {
    const mount = app.slice(app.indexOf('<AskGenie') - 400, app.indexOf('<AskGenie'))
    expect(mount).toContain("!$authRequired")
    expect(mount).toContain("page !== 'onboarding'")
    expect(mount).toContain("page !== 'chat'")
  })
})
