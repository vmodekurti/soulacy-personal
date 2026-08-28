// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { tick } from 'svelte'
import TriggerSettings from './TriggerSettings.svelte'
import GenerationTrigger from './GenerationTrigger.svelte'
import {
  applyGenerationTrigger,
  deliveryModePatch,
  deliveryModeOf,
  intentWithGenerationTrigger,
  routeSummary,
  toggleChannelPatch,
  triggerChannelPatch,
  triggerTypePatch,
} from './triggersettings.js'

let component
let target

afterEach(() => {
  if (component) component.$destroy()
  if (target) target.remove()
  component = null
  target = null
})

function mount(props, Component = TriggerSettings) {
  target = document.createElement('div')
  document.body.appendChild(target)
  component = new Component({ target, props })
  return target
}

describe('GenerationTrigger', () => {
  it('offers GUI Chat as an explicit inbound and outbound conversation', () => {
    const changes = []
    const root = mount({
      selection: { type: 'auto', delivery: 'auto' },
      channels: [],
      onChange: (value) => changes.push(value),
    }, GenerationTrigger)
    const type = root.querySelector('#generation-trigger-type')
    type.value = 'chat'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(changes.at(-1)).toMatchObject({ type: 'chat', delivery: 'reply' })
  })

  it('names the inferred trigger and makes the correction path explicit', async () => {
    const changes = []
    const root = mount({
      selection: { type: 'auto', delivery: 'auto' },
      inferredTrigger: 'schedule',
      channels: [],
      onChange: (value) => changes.push(value),
    }, GenerationTrigger)

    expect(root.textContent).toContain('Studio guessed: Cron schedule')
    expect(root.textContent).toContain('If that guess is wrong')
    expect(root.querySelector('#generation-trigger-type option[value="auto"]').textContent)
      .toContain('Studio’s guess — Cron schedule')

    const type = root.querySelector('#generation-trigger-type')
    type.value = 'manual'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    component.$set({ selection: changes.at(-1) })
    await tick()

    expect(root.textContent).toContain('Your selection overrides Studio’s guess')
    expect(changes.at(-1)).toMatchObject({ type: 'manual', delivery: 'reply' })
  })

  it('keeps contextual reply for manual invocation and drops it for a schedule', async () => {
    const changes = []
    const root = mount({
      selection: { type: 'auto', delivery: 'auto' },
      channels: [{ id: 'telegram', name: 'Telegram' }],
      onChange: (value) => changes.push(value),
    }, GenerationTrigger)
    const type = root.querySelector('#generation-trigger-type')
    type.value = 'channel'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(changes.at(-1).delivery).toBe('reply')

    component.$set({ selection: changes.at(-1) })
    await tick()
    type.value = 'manual'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(changes.at(-1).delivery).toBe('reply')

    component.$set({ selection: changes.at(-1) })
    await tick()
    type.value = 'schedule'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(changes.at(-1).delivery).toBe('auto')
  })
})

describe('TriggerSettings', () => {
  it('makes GUI Chat-only conversation explicit', () => {
    const root = mount({
      workflow: { trigger: { type: 'chat' }, delivery_mode: 'reply' },
      channels: [{ id: 'telegram', name: 'Telegram' }],
    })
    expect(root.querySelector('#studio-trigger-type').value).toBe('chat')
    expect(root.querySelector('#studio-delivery-mode').value).toBe('reply')
    expect(root.textContent).toContain('Inbound and outbound stay in the same Soulacy GUI Chat conversation')
    expect(root.textContent).not.toContain('Output channels')
  })

  it('changes a manual agent to cron and reveals only schedule settings', async () => {
    const patches = []
    const workflow = { trigger: { type: 'manual', config: {} }, unattended: true }
    const root = mount({ workflow, channels: [{ id: 'telegram', name: 'Telegram' }], onChange: (p) => patches.push(p) })

    const type = root.querySelector('#studio-trigger-type')
    type.value = 'schedule'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(patches.at(-1)).toEqual({
      trigger: { type: 'schedule', config: {} },
      delivery_mode: 'none',
      channels: [],
      output: null,
      unattended: false,
    })

    component.$set({ workflow: { ...workflow, ...patches.at(-1) } })
    await tick()
    expect(root.querySelector('#studio-trigger-cron')).toBeTruthy()
    expect(root.querySelector('#studio-trigger-channel')).toBeFalsy()
    expect(root.textContent).toContain('Allow unattended execution')
  })

  it('disables channel mode until a channel is configured', () => {
    const root = mount({ workflow: { trigger: { type: 'manual' } }, channels: [] })
    const option = root.querySelector('option[value="channel"]')
    expect(option.disabled).toBe(true)
    expect(option.textContent).toContain('configure a channel first')
  })

  it('shows a saved channel binding when the Studio-only hint was not persisted', () => {
    const root = mount({
      workflow: { trigger: { type: 'channel', config: {} }, channels: ['telegram'] },
      channels: [{ id: 'telegram', name: 'Telegram' }],
    })
    expect(root.querySelector('#studio-trigger-channel').value).toBe('telegram')
  })
})

describe('trigger settings transitions', () => {
  it('clears schedule-only settings when switching to manual', () => {
    const draft = {
      trigger: { type: 'schedule', config: { cron: '0 8 * * *' } },
      output: { channel: 'telegram', to: '123' },
      unattended: true,
    }
    expect(triggerTypePatch(draft, 'manual')).toEqual({
      trigger: { type: 'manual', config: {} },
      output: null,
      unattended: false,
    })
  })

  it('does not invent a cron or retain stale delivery when manual becomes scheduled', () => {
    expect(triggerTypePatch({ trigger: { type: 'manual' }, output: { to: 'old' } }, 'schedule')).toEqual({
      trigger: { type: 'schedule', config: {} },
      output: null,
      unattended: false,
    })
  })

  it('binds the selected inbound channel to the agent', () => {
    expect(triggerChannelPatch({ channels: ['slack'] }, 'telegram')).toEqual({
      trigger: { type: 'channel', config: { channel: 'telegram' } },
      channels: ['slack', 'telegram'],
    })
  })

  it('clears dependent channel and delivery settings when a binding is removed', () => {
    const draft = {
      trigger: { type: 'channel', config: { channel: 'telegram' } },
      channels: ['telegram', 'slack'],
      output: { channel: 'telegram', to: '123', bot_name: 'news' },
    }
    expect(toggleChannelPatch(draft, 'telegram', false)).toEqual({
      channels: ['slack'],
      trigger: { type: 'channel', config: {} },
      output: { channel: '', to: '123', bot_name: '' },
    })
  })

  it('leaves prompt-inferred triggers unchanged when no override was chosen', () => {
    const draft = { trigger: { type: 'schedule', config: { cron: '0 7 * * *' } } }
    expect(applyGenerationTrigger(draft, { type: 'auto' })).toBe(draft)
  })

  it('makes an explicit creation-time trigger authoritative after generation', () => {
    const generated = {
      trigger: { type: 'schedule', config: { cron: '0 7 * * *' } },
      output: { channel: 'telegram', to: '123' },
      unattended: true,
      channels: ['telegram'],
    }
    expect(applyGenerationTrigger(generated, { type: 'channel', channel: 'slack' })).toEqual({
      trigger: { type: 'channel', config: { channel: 'slack' } },
      output: null,
      unattended: false,
      channels: ['telegram', 'slack'],
    })
  })

  it('grounds refinement and generation in a conversational channel choice', () => {
    const text = intentWithGenerationTrigger('Build a weather expert.', {
      type: 'channel', channel: 'telegram',
    })
    expect(text).toContain('Build a weather expert.')
    expect(text).toContain('message arrives on telegram')
    expect(text).toContain('Do not convert this into a scheduled job')
  })

  it('grounds GUI Chat-only generation without external delivery', () => {
    const selection = { type: 'chat', delivery: 'reply' }
    expect(intentWithGenerationTrigger('Build a flight assistant.', selection))
      .toContain('same Soulacy GUI Chat conversation')
    expect(applyGenerationTrigger({
      trigger: { type: 'schedule' }, channels: ['telegram'], output: { channel: 'telegram' },
    }, selection)).toMatchObject({
      trigger: { type: 'chat', config: {} }, delivery_mode: 'reply', channels: [], output: null,
    })
  })

  it('does not alter the prompt while trigger inference is enabled', () => {
    expect(intentWithGenerationTrigger('Build a weather expert.', { type: 'auto' }))
      .toBe('Build a weather expert.')
  })

  it('overrides an inferred Telegram destination in both prompt and draft', () => {
    const selection = { type: 'auto', delivery: 'slack', destination: '#weather' }
    expect(intentWithGenerationTrigger('Send updates to Telegram.', selection))
      .toContain('through slack to #weather; do not substitute another channel')
    expect(applyGenerationTrigger({
      trigger: { type: 'schedule', config: { cron: '0 7 * * *' } },
      channels: ['telegram'], output: { channel: 'telegram', to: 'old' },
    }, selection)).toEqual({
      trigger: { type: 'schedule', config: { cron: '0 7 * * *' } },
      delivery_mode: 'outbound',
      channels: ['telegram', 'slack'], output: { channel: 'slack', to: '#weather' },
    })
  })

  it('preserves same-channel reply as structured delivery intent', () => {
    expect(applyGenerationTrigger({
      trigger: { type: 'manual', config: {} },
      channels: ['http'],
      output: { channel: 'telegram', to: 'old' },
    }, { type: 'manual', delivery: 'reply' })).toEqual({
      trigger: { type: 'manual', config: {} },
      channels: [],
      delivery_mode: 'reply',
      output: null,
      unattended: false,
    })
  })

  it('switches contextual delivery without requiring or retaining an output', () => {
    expect(deliveryModePatch({ output: { channel: 'telegram' } }, 'reply')).toEqual({
      delivery_mode: 'reply', output: null,
    })
    expect(deliveryModePatch({}, 'outbound')).toEqual({ delivery_mode: 'outbound' })
  })

  it('recognizes same-channel intent in drafts saved before delivery mode existed', () => {
    expect(deliveryModeOf({
      trigger: { type: 'manual' },
      intent: 'Answer the question and reply directly in the same conversation.',
    })).toBe('reply')
    expect(deliveryModeOf({ trigger: { type: 'schedule' } })).toBe('none')
  })

  it('describes valid input/output routes in user-facing terms', () => {
    expect(routeSummary('chat', 'reply')).toBe('Soulacy GUI Chat message → Same Soulacy GUI Chat conversation')
    expect(routeSummary('channel', 'reply')).toBe('Connected channel message → Same inbound channel conversation')
    expect(routeSummary('webhook', 'reply')).toBe('Inbound webhook request → HTTP response to the webhook caller')
    expect(routeSummary('manual', 'reply')).toBe('Manual or programmatic invocation → Return to the manual/API caller')
    expect(routeSummary('schedule', 'none')).toBe('Cron schedule → Runs / Activity only')
    expect(routeSummary('schedule', 'telegram', 'Telegram')).toBe('Cron schedule → Send to fixed Telegram destination')
    expect(routeSummary('chat', 'telegram', 'Telegram')).toBe('Soulacy GUI Chat message → Same Soulacy GUI Chat conversation + also send to fixed Telegram destination')
  })
})
