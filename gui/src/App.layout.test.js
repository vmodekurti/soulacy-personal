import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const app = readFileSync(fileURLToPath(new URL('./App.svelte', import.meta.url)), 'utf8')

describe('workspace shell', () => {
  it('keeps the everyday workspace visible and moves the complete map into a launcher', () => {
    expect(app).toContain("const railIds = ['feed', 'chat', 'agents', 'missions', 'schedule', 'connectors', 'knowledge', 'templates', 'reports', 'config']")
    expect(app).toContain('aria-label="Primary workspace navigation"')
    expect(app).toContain('aria-label="All Soulacy tools"')
    expect(app).toContain('placeholder="Find a screen"')
    expect(app).toContain('grid-template-columns: 214px minmax(0, 1fr)')
    expect(app).toContain("feed: ['⌂', 'Home']")
  })

  it('opens chat first by default and keeps the workspace one switch away', () => {
    const stores = readFileSync(fileURLToPath(new URL('./lib/stores.js', import.meta.url)), 'utf8')
    expect(stores).toContain("localWritable('soulacy_shell_mode', 'focus')")
    expect(app).toContain('class:focus-shell={focus}')
    // The conversation stays mounted while a screen is open beside it.
    expect(app).toContain('<svelte:component this={ChatComponent} bind:this={chatRef} focusShell>')
    expect(app).toContain('aria-label="Layout"')
    // The tour points at sidebar entries, so it forces the workspace.
    expect(app).toContain("$shellMode === 'focus' && !$walkthrough.active")
  })

  it('keeps the chat-first top bar to a menu, the brand, a new conversation and the account', () => {
    const chat = readFileSync(fileURLToPath(new URL('./pages/Chat.svelte', import.meta.url)), 'utf8')
    expect(app).toContain('class="focus-menu"')
    expect(app).toContain('aria-label="Start a new conversation"')
    // The chat page's header is hidden in this shell, so what it held must be
    // reachable from the shell: these are the hooks the bar and menu call.
    expect(chat).toContain('export function newChat()')
    expect(chat).toContain('export function showHistory()')
    expect(chat).toContain('export function showDetails()')
    expect(chat).toContain('.page.modern-chat.focus > .page-header { display: none; }')
    expect(chat).toContain('Your assistant. Your context. Your control.')
  })

  it('ships day and night themes from the same semantic tokens', () => {
    expect(app).toContain("let theme = 'light'")
    expect(app).toContain(':global(:root[data-theme="dark"])')
    expect(app).toContain('aria-label="Toggle light and dark"')
  })
})
