// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { tick } from 'svelte'
import TriggerSettings from './TriggerSettings.svelte'
import {
  applyGenerationTrigger,
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

function mount(props) {
  target = document.createElement('div')
  document.body.appendChild(target)
  component = new TriggerSettings({ target, props })
  return target
}

describe('TriggerSettings', () => {
  it('changes a manual agent to cron and reveals only schedule settings', async () => {
    const patches = []
    const workflow = { trigger: { type: 'manual', config: {} }, unattended: true }
    const root = mount({ workflow, channels: [{ id: 'telegram', name: 'Telegram' }], onChange: (p) => patches.push(p) })

    const type = root.querySelector('#studio-trigger-type')
    type.value = 'schedule'
    type.dispatchEvent(new Event('change', { bubbles: true }))
    expect(patches.at(-1)).toEqual({
      trigger: { type: 'schedule', config: {} },
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
})
