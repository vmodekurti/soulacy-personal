import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('./App.svelte', import.meta.url)), 'utf8')
const chat = readFileSync(fileURLToPath(new URL('./pages/Chat.svelte', import.meta.url)), 'utf8')

describe('mobile-first application foundation', () => {
  it('keeps navigation reachable with a safe-area command bar and bottom tabs', () => {
    expect(app).toContain('class="mobile-tabs"')
    expect(app).toContain('aria-label="Primary navigation"')
    expect(app).toContain('height: 100dvh')
    expect(app).toContain('env(safe-area-inset-top)')
    expect(app).toContain('env(safe-area-inset-bottom)')
    expect(app).toContain("['dashboard', 'studio', 'agents', 'chat']")
  })

  it('provides touch-sized controls and prevents iOS form zoom', () => {
    expect(app).toContain('min-height: 48px')
    expect(app).toContain('font-size: 16px !important')
    expect(app).toContain('width: min(320px, 88vw)')
  })

  it('treats conversations as a dismissible mobile sheet and keeps the composer usable', () => {
    expect(chat).toContain('class="chat-sidebar-backdrop"')
    expect(chat).toContain("window.matchMedia('(max-width: 720px)')")
    expect(chat).toContain('if (mobileViewport) chatListHidden = true')
    expect(chat).toContain('.composer-voice, .saved-prompts-btn { display: none; }')
    expect(chat).toContain('width: 44px; height: 44px')
  })
})
