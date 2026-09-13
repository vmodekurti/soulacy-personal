import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('./App.svelte', import.meta.url)), 'utf8')
const chat = readFileSync(fileURLToPath(new URL('./pages/Chat.svelte', import.meta.url)), 'utf8')
const studio = readFileSync(fileURLToPath(new URL('./pages/Studio.svelte', import.meta.url)), 'utf8')
const studioCss = readFileSync(fileURLToPath(new URL('./pages/Studio.css', import.meta.url)), 'utf8')
const reports = readFileSync(fileURLToPath(new URL('./pages/Reports.svelte', import.meta.url)), 'utf8')

describe('mobile-first application foundation', () => {
  it('constrains reports to the flex container instead of shrink-wrapping wide tables', () => {
    expect(reports).toMatch(/\.reports\s*\{[^}]*width:100%;[^}]*box-sizing:border-box;/)
    expect(reports).toContain('.table-scroll { overflow-x:auto; max-width:100%; }')
    expect(reports).toContain('Keyboard focus enables horizontal scrolling')
  })
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

  it('isolates Studio header styles from the application command bar', () => {
    expect(studio).toContain('class="studio-topbar"')
    expect(studioCss).toContain('.studio-topbar')
    expect(studioCss).not.toMatch(/(^|[\s,{])\.topbar\b/m)
  })

  it('uses an app-like Chat shell instead of stacking desktop mobile chrome', () => {
    expect(app).toContain('class:chat-route={page === \'chat\'}')
    expect(app).toContain('.layout.chat-route .topbar { display: none; }')
    expect(chat).toContain('rows={mobileViewport ? 1 : 2}')
    expect(chat).toContain('mobileViewport ? `Message ${activeThread?.agentId ? agentName(activeThread.agentId) : \'agent\'}…`')
    expect(chat).toContain('.modern-chat .msg-row.sys .bubble { width: 100%; max-width: 100%;')
    expect(chat).toContain('.modern-chat :global(.markdown-body table)')
    expect(chat).toContain('overflow-x: auto; overscroll-behavior-inline: contain;')
  })
})
