import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const chat = readFileSync(fileURLToPath(new URL('./Chat.svelte', import.meta.url)), 'utf8')

describe('focused Chat workspace', () => {
  it('keeps primary navigation compact while retaining secondary actions', () => {
    expect(chat).toContain('class="page modern-chat"')
    expect(chat).toContain('class="chat-more-menu"')
    expect(chat).toContain('Model &amp; generation controls')
    expect(chat).toContain('Export Markdown')
    expect(chat).toContain('Share conversation')
  })

  it('provides the immersive voice session controls', () => {
    expect(chat).toContain('class="voice-session"')
    expect(chat).toContain('SOULACY SPEAKING')
    expect(chat).toContain('Minimize voice session')
    expect(chat).toContain('END SESSION')
    expect(chat).toContain('Stop this response')
  })

  it('uses the available desktop width for rich responses and the composer', () => {
    expect(chat).toContain('width: min(1800px, calc(100% - 4rem))')
    expect(chat).toContain('width: 100%; max-width: 100%; padding: 1rem 1.15rem')
    expect(chat).toContain('width: min(1500px, calc(100% - 4rem))')
    expect(chat).toContain('max-width: 92ch')
  })

  it('submits the composer text from the send button instead of the click event', () => {
    expect(chat).toContain("const hasExplicitText = typeof textArg === 'string'")
    expect(chat).toContain('const text = (hasExplicitText ? textArg : input).trim()')
    expect(chat).toContain('on:click={() => send()}')
    expect(chat).toContain('aria-label="Send message"')
  })

  it('keeps the activity explanation collapsed until the user opens it', () => {
    expect(chat).toContain('const thinking = { open: false, events: [] }')
    expect(chat).toContain('const nextThinking = { open: t.thinking?.open ?? false, events: merged.slice(-80) }')
    expect(chat).toContain('How this answer was made')
    expect(chat).toContain('An auditable summary of decisions, tools, and evidence.')
    expect(chat).not.toContain('const thinking = { open: true, events: [] }')
  })

  it('keeps active chat runs and every turn trace across page navigation', () => {
    expect(chat).toContain('thinking: m.thinking ? { ...m.thinking, open: false } : null')
    expect(chat).toContain('if (Object.keys($chatThreads).length === 0) restoreThreads()')
    expect(chat).toContain('const live = Object.values($chatThreads).find(t => t.activeRunKey === key)')
    expect(chat).toContain("return live?.id || ''")
    expect(chat).toContain('backfillAllThinking(id, t.agentId, t.sessionId)')
    expect(chat).toContain('await backfillAllThinking(activeThread.id, activeThread.agentId, activeThread.sessionId)')
    expect(chat).not.toContain('await backfillLatestThinking(')
  })

  it('makes complete conversation-history search reachable on mobile', () => {
    expect(chat).toContain('Search conversation history</button>')
    expect(chat).toContain("api.history.search(historyQuery.trim(), '', 50)")
    expect(chat).toContain('Search questions and answers')
    expect(chat).toContain('position: fixed; z-index: 55; top: calc(58px + env(safe-area-inset-top))')
  })
})
