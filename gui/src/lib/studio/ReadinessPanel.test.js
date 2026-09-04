// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import ReadinessPanel from './ReadinessPanel.svelte'

let component
let target

afterEach(() => {
  if (component) component.$destroy()
  if (target) target.remove()
  component = null
  target = null
})

function render(item, props = {}) {
  target = document.createElement('div')
  document.body.appendChild(target)
  component = new ReadinessPanel({
    target,
    props: {
      report: {
        ok: false,
        sections: [{ id: 'contract', title: 'Generation contract', status: 'block' }],
        blockers: [{ section: 'contract', ...item }],
        warnings: [],
        ready: [],
        unknown: [],
      },
      ...props,
    },
  })
  return target
}

describe('readiness destination recovery', () => {
  it('offers configured destinations inline for a missing output channel', async () => {
    const onDestination = vi.fn()
    const el = render({
      action: 'open_delivery',
      message: 'No routable output channel or schedule output is configured.',
      fix: 'Pick a destination.',
    }, {
      destinations: [
        { id: 'telegram', name: 'Telegram' },
        { id: 'slack', name: 'Slack' },
      ],
      onDestination,
    })

    const select = el.querySelector('.rp-destination select')
    expect(select).toBeTruthy()
    expect([...select.options].map((o) => o.textContent)).toContain('Telegram')
    select.value = 'telegram'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.resolve()
    expect(onDestination).toHaveBeenCalledWith('telegram', expect.objectContaining({ action: 'open_delivery' }))
  })

  it('keeps the settings action for a channel that is selected but unconfigured', () => {
    const el = render({
      action: 'open_delivery',
      actionParams: { channel: 'telegram' },
      actionLabel: 'Configure Telegram',
      message: 'Telegram is not configured.',
    }, {
      destinations: [{ id: 'slack', name: 'Slack' }],
    })

    expect(el.querySelector('.rp-destination')).toBeNull()
    expect(el.querySelector('button')?.textContent).not.toBe('')
    expect(el.textContent).toContain('Configure Telegram')
  })
})
