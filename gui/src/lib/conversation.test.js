// @vitest-environment jsdom
//
// The Genie conversation has to survive leaving the page.
//
// Chat is no longer in the simple sidebar, so this screen is the only
// conversational surface most people will use. A page that empties itself the
// moment someone glances at Deployed is not a conversation.

import { describe, it, expect, beforeEach } from 'vitest'
import { get } from 'svelte/store'
import { genieConversation } from './stores.js'

beforeEach(() => {
  sessionStorage.clear()
  genieConversation.set(null)
})

describe('the saved conversation', () => {
  it('survives a reload', () => {
    genieConversation.set({ turns: [{ role: 'you', text: 'hello' }], sessionId: 's1', phase: 'answer' })
    const raw = sessionStorage.getItem('soulacy_genie_convo')
    expect(raw, 'it should be written to session storage').toBeTruthy()
    expect(JSON.parse(raw).turns[0].text).toBe('hello')
  })

  it('clears itself when the conversation is reset', () => {
    genieConversation.set({ turns: [{ role: 'you', text: 'hello' }] })
    genieConversation.set(null)
    expect(sessionStorage.getItem('soulacy_genie_convo')).toBeNull()
    expect(get(genieConversation)).toBeNull()
  })

  // Session storage, not local: a conversation is content, and content should
  // not outlive the browser session on a shared machine.
  it('does not persist to local storage', () => {
    genieConversation.set({ turns: [{ role: 'you', text: 'private' }] })
    expect(localStorage.getItem('soulacy_genie_convo')).toBeNull()
  })
})
