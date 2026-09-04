import { describe, it, expect } from 'vitest'
import {
  strategyLabel, specRows, specBlockers, specQuestions, specReady, changeSummary,
  detectChannel, deliveryPrompt, isDeliveryQuestion, knownDestinations,
  specWithTrigger,
} from './buildspecview.js'

const spec = {
  intent: 'daily AI podcast',
  trigger: 'Weekdays at 7:00 AM',
  schedule_text: '0 7 * * 1-5',
  inputs: ['HBR.org', 'MIT Technology Review', 'Gartner.com'],
  stages: [
    { name: 'Web search', detail: 'three sources', parallel: true },
    { name: 'Curate source pack' },
  ],
  outputs: ['mp3 briefing'],
  delivery: ['Telegram'],
  integrations: ['web_search', 'notebooklm'],
  security: ['authenticated sources'],
  questions: [
    { id: 'q1', question: 'Which Telegram chat?', blocker: true },
    { id: 'q2', question: 'How long should it be?', blocker: false },
  ],
}

describe('strategyLabel', () => {
  it('labels each strategy and returns empty for unknown', () => {
    expect(strategyLabel('workflow')).toBe('Workflow (fixed flow)')
    expect(strategyLabel('plan_execute')).toBe('Plan-Execute')
    expect(strategyLabel('nonsense')).toBe('')
    expect(strategyLabel(undefined)).toBe('')
  })
})

describe('specRows', () => {
  it('returns every row even when a section is empty, so omissions are visible', () => {
    const rows = specRows({}, null)
    const keys = rows.map((r) => r.key)
    expect(keys).toContain('trigger')
    expect(keys).toContain('delivery')
    // An absent delivery is the thing the user most needs to notice.
    expect(rows.find((r) => r.key === 'delivery').empty).toBe(true)
  })

  it('does not emit a security row when there is nothing to report', () => {
    // An empty security row reads as reassurance we have no basis to give.
    const rows = specRows({}, null)
    expect(rows.find((r) => r.key === 'security')).toBeUndefined()
  })

  it('emits a security row when the spec found something', () => {
    const rows = specRows(spec, null)
    expect(rows.find((r) => r.key === 'security').value).toContain('authenticated')
  })

  it('combines trigger and schedule into one readable line', () => {
    const rows = specRows(spec, null)
    const t = rows.find((r) => r.key === 'trigger')
    expect(t.value).toContain('Weekdays at 7:00 AM')
    expect(t.value).toContain('0 7 * * 1-5')
    expect(t.empty).toBe(false)
  })

  it('marks parallel stages so fan-out is visible before generating', () => {
    const rows = specRows(spec, null)
    const work = rows.find((r) => r.key === 'stages')
    expect(work.lines[0]).toContain('runs in parallel')
    expect(work.lines[1]).not.toContain('runs in parallel')
  })

  it('carries the strategy and its rationale', () => {
    const rows = specRows(spec, { mode: 'workflow', rationale: 'fixed schedule' })
    const s = rows.find((r) => r.key === 'strategy')
    expect(s.value).toBe('Workflow (fixed flow)')
    expect(s.detail).toBe('fixed schedule')
  })

  it('survives a null spec', () => {
    expect(() => specRows(null, null)).not.toThrow()
  })
})

describe('specBlockers / specQuestions', () => {
  it('separates blocking questions from clarifying ones', () => {
    expect(specBlockers(spec).map((q) => q.id)).toEqual(['q1'])
    expect(specQuestions(spec).map((q) => q.id)).toEqual(['q2'])
  })

  it('prefers an explicit blockers list when the server sent one', () => {
    const s = { blockers: [{ id: 'b1' }], questions: [{ id: 'q1', blocker: true }] }
    expect(specBlockers(s).map((q) => q.id)).toEqual(['b1'])
  })

  it('does not double-list a question that is also in blockers', () => {
    const s = { blockers: [{ id: 'q1' }], questions: [{ id: 'q1' }, { id: 'q2' }] }
    expect(specQuestions(s).map((q) => q.id)).toEqual(['q2'])
  })

  it('handles a spec with no questions at all', () => {
    expect(specBlockers({})).toEqual([])
    expect(specQuestions({})).toEqual([])
  })
})

describe('specWithTrigger', () => {
  const scheduled = {
    trigger: 'schedule', schedule: '0 8 * * *', schedule_text: 'every morning',
    delivery: ['Telegram'], security: ['sends messages on your behalf to Telegram'],
    questions: [
      { id: 'schedule_time', field: 'schedule', blocker: true },
      { id: 'destination', field: 'delivery', blocker: true },
      { id: 'sources', field: 'inputs', blocker: false },
    ],
    blockers: [
      { id: 'schedule_time', field: 'schedule', blocker: true },
      { id: 'destination', field: 'delivery', blocker: true },
    ],
  }

  it('turns an inferred schedule into a same-channel conversational spec', () => {
    const next = specWithTrigger(scheduled, { type: 'channel', channel: 'telegram' })
    expect(next.trigger).toBe('incoming telegram message')
    expect(next.schedule).toBe('')
    expect(next.delivery).toEqual(['Reply on telegram'])
    expect(next.blockers).toEqual([])
    expect(next.questions.map((q) => q.id)).toEqual(['sources'])
    expect(next.ready).toBe(true)
    expect(scheduled.schedule).toBe('0 8 * * *') // the server response is not mutated
  })

  it('uses the operator cron and keeps delivery requirements for scheduled runs', () => {
    const next = specWithTrigger(scheduled, { type: 'schedule', cron: '15 9 * * 1-5' })
    expect(next.schedule).toBe('15 9 * * 1-5')
    expect(next.delivery).toEqual(['Telegram'])
    expect(next.blockers.map((q) => q.id)).toEqual(['destination'])
  })

  it('lets an explicit destination replace the channel inferred from the prompt', () => {
    const next = specWithTrigger(scheduled, {
      type: 'auto', delivery: 'slack', destination: '#weather',
    })
    expect(next.delivery).toEqual(['slack → #weather'])
    expect(next.blockers.map((q) => q.id)).toEqual(['schedule_time'])
  })

  it('leaves prompt inference untouched in auto mode', () => {
    expect(specWithTrigger(scheduled, { type: 'auto' })).toBe(scheduled)
  })
})

describe('specReady', () => {
  it('trusts an explicit ready flag', () => {
    expect(specReady({ ready: true, questions: [{ blocker: true }] })).toBe(true)
    expect(specReady({ ready: false })).toBe(false)
  })
  it('falls back to whether any blocker remains', () => {
    expect(specReady(spec)).toBe(false)
    expect(specReady({ questions: [{ blocker: false }] })).toBe(true)
  })
  it('is false for a missing spec', () => {
    expect(specReady(null)).toBe(false)
  })
})

describe('detectChannel', () => {
  it('recognises each adapter from the spec text', () => {
    expect(detectChannel('Telegram')).toBe('telegram')
    expect(detectChannel(['Slack #general'])).toBe('slack')
    expect(detectChannel('Discord')).toBe('discord')
    expect(detectChannel('whatsapp_web')).toBe('whatsapp')
    expect(detectChannel('email digest')).toBe('email')
    expect(detectChannel('SMS via Twilio')).toBe('sms')
  })

  it('does not read "webhook" as a bare web match', () => {
    expect(detectChannel('HTTP (webhook)')).toBe('http')
  })

  it('returns empty when the channel is unknown or absent', () => {
    expect(detectChannel('')).toBe('')
    expect(detectChannel(null)).toBe('')
    expect(detectChannel('carrier pigeon')).toBe('')
  })
})

describe('deliveryPrompt', () => {
  it('asks for a Telegram chat in the format Telegram actually uses', () => {
    const p = deliveryPrompt('Telegram')
    expect(p.channel).toBe('telegram')
    expect(p.question).toContain('Telegram')
    // The point of the whole function: the user is told the FORM and where to
    // find it, not just asked an open question.
    expect(p.placeholder).toContain('@')
    expect(p.help).toMatch(/getidsbot|userinfobot/)
    expect(p.needsDestination).toBe(true)
  })

  it('asks for a Slack channel with its C-prefixed id', () => {
    const p = deliveryPrompt('Slack')
    expect(p.placeholder).toContain('#')
    expect(p.help).toContain('C')
  })

  it('asks for E.164 for WhatsApp and SMS', () => {
    expect(deliveryPrompt('whatsapp').help).toContain('E.164')
    expect(deliveryPrompt('sms').help).toContain('E.164')
  })

  it('does not demand a destination for console output', () => {
    const p = deliveryPrompt('console')
    expect(p.needsDestination).toBe(false)
  })

  it('falls back to a generic ask rather than guessing a format', () => {
    const p = deliveryPrompt('carrier pigeon')
    expect(p.channel).toBe('')
    expect(p.needsDestination).toBe(true)
    expect(p.help).toContain('could not tell')
  })
})

describe('knownDestinations', () => {
  const channels = [{
    id: 'telegram',
    settings: { default_output_to: '-1001111', allowed_chat_ids: '-1002222, @newsroom , ' },
    bots: [{ bot_name: 'Digest', default_output_to: '-1003333', allowed_chat_ids: '-1004444' }],
  }, {
    id: 'slack',
    settings: { default_output_to: 'C0999' },
  }]

  it('puts the configured default first — the most likely answer', () => {
    const d = knownDestinations(channels, 'telegram')
    expect(d[0].value).toBe('-1001111')
    expect(d[0].label).toContain('default destination')
  })

  it('includes the allow-list and per-bot destinations', () => {
    const values = knownDestinations(channels, 'telegram').map((d) => d.value)
    expect(values).toContain('-1002222')
    expect(values).toContain('@newsroom')
    expect(values).toContain('-1003333')
    expect(values).toContain('-1004444')
  })

  it('labels a per-bot destination with the bot it belongs to', () => {
    const d = knownDestinations(channels, 'telegram').find((x) => x.value === '-1003333')
    expect(d.label).toContain('Digest')
  })

  it('trims and drops empty CSV entries', () => {
    const values = knownDestinations(channels, 'telegram').map((d) => d.value)
    expect(values).not.toContain('')
    expect(values.every((v) => v === v.trim())).toBe(true)
  })

  it('dedupes a destination that appears in more than one place', () => {
    const dup = [{ id: 'telegram', settings: { default_output_to: '-1', allowed_chat_ids: '-1,-2' } }]
    expect(knownDestinations(dup, 'telegram').map((d) => d.value)).toEqual(['-1', '-2'])
  })

  it('matches the channel id case-insensitively', () => {
    expect(knownDestinations(channels, 'Telegram')).toHaveLength(5)
  })

  it('accepts either a bare array or the {channels:[…]} envelope', () => {
    expect(knownDestinations({ channels }, 'slack')).toHaveLength(1)
  })

  it('returns empty when nothing is configured, so the caller can fall back', () => {
    // An empty dropdown looks broken; the caller needs to know to ask for a
    // raw ID instead.
    expect(knownDestinations(channels, 'discord')).toEqual([])
    expect(knownDestinations(null, 'telegram')).toEqual([])
    expect(knownDestinations(channels, '')).toEqual([])
  })
})

describe('isDeliveryQuestion', () => {
  it('matches on an explicit field name', () => {
    expect(isDeliveryQuestion({ field: 'delivery' })).toBe(true)
    expect(isDeliveryQuestion({ field: 'channel_destination' })).toBe(true)
  })
  it('matches the generic prose question', () => {
    expect(isDeliveryQuestion({ question: 'Where exactly should the result be delivered?' })).toBe(true)
  })
  it('does not match unrelated questions', () => {
    expect(isDeliveryQuestion({ question: 'Which sources should it read?' })).toBe(false)
    expect(isDeliveryQuestion(null)).toBe(false)
  })
})

describe('changeSummary', () => {
  it('returns null when nothing was compared — "cannot say" is not "no change"', () => {
    expect(changeSummary({ diff: [] })).toBeNull()
    expect(changeSummary(null)).toBeNull()
  })

  it('reports a material change with its diff', () => {
    const c = changeSummary({
      compared: true, materially_different: true,
      diff: [{ field: 'delivery', before: '', after: 'Telegram' }],
    })
    expect(c.material).toBe(true)
    expect(c.changes[0].kind).toBe('added')
    expect(c.summary).toContain('1 change')
  })

  it('says plainly when refining changed nothing material', () => {
    const c = changeSummary({ compared: true, materially_different: false, diff: [] })
    expect(c.material).toBe(false)
    expect(c.summary).toContain('did not materially change')
  })

  it('infers change kinds from before/after presence', () => {
    const c = changeSummary({
      compared: true, materially_different: true,
      diff: [
        { field: 'a', before: 'x', after: 'y' },
        { field: 'b', before: 'x' },
      ],
    })
    expect(c.changes[0].kind).toBe('changed')
    expect(c.changes[1].kind).toBe('removed')
  })
})
