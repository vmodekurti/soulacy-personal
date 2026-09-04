import { describe, it, expect } from 'vitest'
import { entryIdForMessage, nextBranchLabel, entriesToMessages } from './chatbranch.js'

const entries = [
  { id: 10, role: 'user', content: 'q1' },
  { id: 11, role: 'assistant', content: 'a1' },
  { id: 12, role: 'user', content: 'q2' },
  { id: 13, role: 'assistant', content: 'a2' },
]

describe('entryIdForMessage', () => {
  it('maps clean user/assistant lists 1:1', () => {
    const messages = [
      { role: 'user', text: 'q1' },
      { role: 'assistant', text: 'a1' },
      { role: 'user', text: 'q2' },
      { role: 'assistant', text: 'a2' },
    ]
    expect(entryIdForMessage(entries, messages, 0)).toBe(10)
    expect(entryIdForMessage(entries, messages, 1)).toBe(11)
    expect(entryIdForMessage(entries, messages, 3)).toBe(13)
  })

  it('skips local system rows when counting', () => {
    const messages = [
      { role: 'user', text: 'q1' },
      { role: 'system', text: '⚠ transient error' },  // not persisted
      { role: 'assistant', text: 'a1' },
    ]
    expect(entryIdForMessage(entries, messages, 2)).toBe(11)
  })

  it('returns null for system rows themselves', () => {
    const messages = [
      { role: 'user', text: 'q1' },
      { role: 'system', text: 'err' },
    ]
    expect(entryIdForMessage(entries, messages, 1)).toBeNull()
  })

  it('returns null when history is shorter than the GUI list', () => {
    const messages = [
      { role: 'user', text: 'q1' },
      { role: 'assistant', text: 'a1' },
      { role: 'user', text: 'not yet persisted' },
    ]
    expect(entryIdForMessage(entries.slice(0, 2), messages, 2)).toBeNull()
  })

  it('tolerates junk input', () => {
    expect(entryIdForMessage(null, [], 0)).toBeNull()
    expect(entryIdForMessage(entries, [{ role: 'user' }], 5)).toBeNull()
    expect(entryIdForMessage(entries, [{ role: 'user' }], -1)).toBeNull()
  })
})

describe('nextBranchLabel', () => {
  it('numbers forks ignoring main', () => {
    expect(nextBranchLabel([{ label: 'main' }])).toBe('fork 1')
    expect(nextBranchLabel([{ label: 'main' }, { label: 'fork 1' }])).toBe('fork 2')
    expect(nextBranchLabel([])).toBe('fork 1')
  })
})

describe('entriesToMessages', () => {
  it('converts persisted entries to GUI messages', () => {
    const msgs = entriesToMessages([
      { id: 1, role: 'user', content: 'hello', created_at: '2026-06-06T10:00:00Z' },
      { id: 2, role: 'assistant', content: 'hi', created_at: '2026-06-06T10:00:05Z' },
      { id: 3, role: 'weird', content: 'skip me' },
    ])
    expect(msgs).toHaveLength(2)
    expect(msgs[0]).toMatchObject({ role: 'user', text: 'hello' })
    expect(msgs[1]).toMatchObject({ role: 'assistant', text: 'hi' })
  })
  it('tolerates null', () => {
    expect(entriesToMessages(null)).toEqual([])
  })
})
