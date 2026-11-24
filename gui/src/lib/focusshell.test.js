import { describe, it, expect } from 'vitest'
import { navPages } from './nav.js'
import { buildBrief, greetingFor, isHomePage, panelLayout, summonMatches } from './focusshell.js'

describe('panelLayout', () => {
  it('gives the conversation the whole window at home', () => {
    expect(isHomePage('chat')).toBe(true)
    expect(panelLayout('chat')).toBe('')
  })
  it('puts ordinary screens beside the conversation and editors in front of it', () => {
    expect(panelLayout('schedule')).toBe('side')
    expect(panelLayout('feed')).toBe('side')
    expect(panelLayout('studio')).toBe('wide')
  })
  it('shows first-run setup alone: there is no model to talk to yet', () => {
    expect(panelLayout('onboarding')).toBe('solo')
  })
  it('opens plugin screens beside the conversation', () => {
    expect(panelLayout('plugin:anything')).toBe('side')
  })
})

describe('summonMatches', () => {
  const ids = (q, opts) => summonMatches(q, opts).map((p) => p.id)

  it('offers the common places before anything is typed', () => {
    expect(ids('')).toEqual(['schedule', 'agents', 'knowledge', 'workboard', 'activity', 'config'])
  })
  it('finds a screen by the word people use, not only its label', () => {
    expect(ids('sched')[0]).toBe('schedule')   // labelled "Automations"
    expect(ids('settings')[0]).toBe('config')
    expect(ids('runs')[0]).toBe('activity')
    expect(ids('memory')[0]).toBe('memory')    // labelled "Learning"
  })
  it('never offers the conversation itself', () => {
    expect(ids('chat')).not.toContain('chat')
  })
  it('can reach every other screen, whatever its menu level', () => {
    for (const p of navPages) {
      if (p.id === 'chat') continue
      expect(ids(p.label, { limit: 99 }), p.id).toContain(p.id)
    }
  })
  it('respects the limit', () => {
    expect(ids('s', { limit: 3 })).toHaveLength(3)
  })
})

describe('buildBrief', () => {
  const now = new Date(2026, 10, 21, 19, 30)
  const at = (h, m = 0) => new Date(2026, 10, 21, h, m).toISOString()

  it('greets by time of day', () => {
    expect(greetingFor(new Date(2026, 0, 1, 8))).toBe('Good morning')
    expect(greetingFor(new Date(2026, 0, 1, 14))).toBe('Good afternoon')
    expect(greetingFor(now)).toBe('Good evening')
  })

  it('says an empty day is empty and shows nothing else', () => {
    const b = buildBrief({ now })
    expect(b.sentence).toBe('Nothing has run yet today, and nothing is waiting on you.')
    expect(b.waiting).toBeNull()
    expect(b).not.toHaveProperty('chips')
  })

  it('reports the latest result and what is running', () => {
    const b = buildBrief({
      now,
      agents: [{ id: 'outlook', name: 'Weekly market outlook' }],
      running: 1,
      runs: [
        { agentId: 'outlook', status: 'completed', ok: true, updatedAt: at(7) },
        { agentId: 'other', status: 'completed', ok: true, updatedAt: at(6) },
        { agentId: 'old', status: 'completed', ok: true, updatedAt: new Date(2026, 10, 20, 9).toISOString() },
        { agentId: 'busy', status: 'running', updatedAt: at(19) },
      ],
    })
    expect(b.sentence).toBe(`Weekly market outlook finished at ${new Date(2026, 10, 21, 7).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}. 1 run is still working.`)
    expect(b.waiting).toBeNull()
  })

  it('does not call a failure a finish', () => {
    const b = buildBrief({ now, runs: [{ agentId: 'x', status: 'failed', ok: false, updatedAt: at(9) }] })
    expect(b.sentence).toContain('x failed at')
  })

  it('shows one waiting thing, the oldest, with who is asking', () => {
    const b = buildBrief({
      now,
      agents: [{ id: 'gifts', name: 'Gift finder' }],
      approvals: [
        { call_id: 'late', agent_id: 'genie', tool: 'calendar_create', reason: 'Add a reminder', created_at: at(10) },
        { call_id: 'early', agent_id: 'gifts', tool: 'send_email', reason: 'Send the shortlist', created_at: at(9) },
      ],
    })
    expect(b.waiting).toEqual({
      callId: 'early',
      lead: '2 things need you. First',
      text: 'Send the shortlist',
      detail: 'Gift finder · send email',
    })
    // The waiting line carries the count, so the sentence does not repeat it.
    expect(b.sentence).toBe('Nothing has run yet today.')
  })

  it('words a single approval without a reason from its tool', () => {
    const b = buildBrief({ now, approvals: [{ call_id: 'c', agent_id: 'a1', tool: 'browser.open' }, { tool: 'no-id' }] })
    expect(b.waiting).toMatchObject({ callId: 'c', lead: 'One thing needs you', text: 'Allow browser open', detail: 'a1 · browser open' })
  })
})
