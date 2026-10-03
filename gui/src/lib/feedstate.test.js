import { describe, expect, it } from 'vitest'
import { applyRemoteFeedState, buildFeedCards, continuationPrompt, filterFeedCards, groupResults, initialReadState } from './feedstate.js'

const cards = [
  { id: 'approval:1', kind: 'approval' },
  { id: 'run:1', kind: 'run', body: 'Done' },
  { id: 'run:2', kind: 'run', body: 'Saved' },
  { id: 'delivery:1', kind: 'delivery', title: 'Brief', body: 'News', unread: true },
]

describe('feed organization', () => {
  it('keeps Home focused and separates results, saved, and archived cards', () => {
    const state = {
      read: new Set(['run:1', 'run:2']),
      saved: new Set(['run:2']),
      archived: new Set(['run:1']),
    }
    expect(filterFeedCards(cards, 'home', state).map(c => c.id)).toEqual(['approval:1', 'delivery:1'])
    expect(filterFeedCards(cards, 'results', state).map(c => c.id)).toEqual(['run:2', 'delivery:1'])
    expect(filterFeedCards(cards, 'unread', state).map(c => c.id)).toEqual(['delivery:1'])
    expect(filterFeedCards(cards, 'saved', state).map(c => c.id)).toEqual(['run:2'])
    expect(filterFeedCards(cards, 'archived', state).map(c => c.id)).toEqual(['run:1'])
  })

  it('collapses a scheduled run and phone delivery into one canonical result', () => {
    const merged = buildFeedCards({
      runs: [{ id: 'run-1', agentId: 'coach', sessionId: 'sched-coach-1', output: 'Run output', status: 'failed', ok: false, startedAt: '2026-10-03T12:00:00Z', trigger: 'cron' }],
      deliveries: [{ id: 'delivery-1', agent_id: 'coach', session_id: 'sched-coach-1', title: 'Daily coach', body: 'Delivered output', created_at: '2026-10-03T12:01:00Z', metadata: { trigger: 'cron' } }],
    })
    expect(merged).toHaveLength(1)
    expect(merged[0]).toMatchObject({ id: 'result:sched-coach-1', title: 'Daily coach', body: 'Delivered output', ok: false, deliveryId: 'delivery-1' })
    expect(merged[0].legacyIds).toEqual(['run:run-1', 'delivery:delivery-1'])
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

  it('migrates old run state to the canonical result and groups results by agent', () => {
    const result = { id: 'result:s1', kind: 'run', agent: 'coach', at: '2026-10-03T12:00:00Z', legacyIds: ['run:r1', 'delivery:d1'] }
    const state = applyRemoteFeedState([result], [{ card_id: 'run:r1', read: false, saved: true, archived: false }])
    expect([...state.saved]).toEqual(['result:s1'])
    expect(groupResults([result], id => id === 'coach' ? 'Daily Coach' : id, state.read)[0]).toMatchObject({ name: 'Daily Coach', unread: 1 })
  })
})
