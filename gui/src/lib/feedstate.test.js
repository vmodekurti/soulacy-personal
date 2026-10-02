import { describe, expect, it } from 'vitest'
import { applyRemoteFeedState, continuationPrompt, filterFeedCards, initialReadState } from './feedstate.js'

const cards = [
  { id: 'approval:1', kind: 'approval' },
  { id: 'run:1', kind: 'run', body: 'Done' },
  { id: 'run:2', kind: 'run', body: 'Saved' },
  { id: 'delivery:1', kind: 'delivery', title: 'Brief', body: 'News', unread: true },
]

describe('feed organization', () => {
  it('keeps approvals in Feed and separates unread, saved, and archived cards', () => {
    const state = {
      read: new Set(['run:1', 'run:2']),
      saved: new Set(['run:2']),
      archived: new Set(['run:1']),
    }
    expect(filterFeedCards(cards, 'feed', state).map(c => c.id)).toEqual(['approval:1', 'run:2', 'delivery:1'])
    expect(filterFeedCards(cards, 'unread', state).map(c => c.id)).toEqual(['delivery:1'])
    expect(filterFeedCards(cards, 'saved', state).map(c => c.id)).toEqual(['run:2'])
    expect(filterFeedCards(cards, 'archived', state).map(c => c.id)).toEqual(['run:1'])
  })

  it('starts with existing work read while preserving unread deliveries', () => {
    expect([...initialReadState(cards)]).toEqual(['run:1', 'run:2'])
  })

  it('hands a result to Genie with its source and useful content', () => {
    expect(continuationPrompt(cards[3], 'Daily Coach')).toContain('Continue this result from Daily Coach:\n\nBrief\n\nNews')
  })

  it('applies durable state over sensible defaults for cards without a stored row', () => {
    const state = applyRemoteFeedState(cards, [
      { card_id: 'run:1', read: false, saved: true, archived: false },
      { card_id: 'delivery:1', read: true, saved: false, archived: true },
    ])
    expect([...state.read]).toEqual(['run:2', 'delivery:1'])
    expect([...state.saved]).toEqual(['run:1'])
    expect([...state.archived]).toEqual(['delivery:1'])
  })
})
