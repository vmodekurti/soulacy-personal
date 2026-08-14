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
})
