// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import UpgradeAction from './UpgradeAction.svelte'

let component
let target

function mount(info, onUpgrade = vi.fn(), onCheck = null) {
  target = document.createElement('div')
  document.body.appendChild(target)
  component = new UpgradeAction({ target, props: { info, onUpgrade, onCheck } })
  return { onUpgrade, onCheck }
}

afterEach(() => {
  component?.$destroy()
  target?.remove()
  component = null
  target = null
})

describe('UpgradeAction', () => {
  it('offers the inline upgrade on a writable host', () => {
    const { onUpgrade } = mount({ upgrade_strategy: 'inline', mode: 'install' })
    const button = target.querySelector('button')
    expect(button.textContent).toMatch(/upgrade now/i)
    button.click()
    expect(onUpgrade).toHaveBeenCalledOnce()
  })

  it('shows redeploy instructions instead of upgrading a managed deployment', async () => {
    const onCheck = vi.fn(async () => ({
      current_version: 'v1.2.2',
      latest_version: 'v1.2.3',
      update_available: true,
    }))
    const { onUpgrade } = mount({
      upgrade_strategy: 'instructions',
      deployment_platform: 'Render',
      upgrade_reason: 'This deployment is image-based.',
      current_version: 'v1.2.2',
      latest_version: 'v1.2.3',
      target_image: 'ghcr.io/vmodekurti/soulacy-personal:1.2.3',
      upgrade_instructions: [
        'Open the Soulacy service in Render.',
        'Choose Manual Deploy, then Deploy latest.',
      ],
      upgrade_help_url: 'https://example.test/upgrades',
    }, vi.fn(), onCheck)

    const button = target.querySelector('button')
    expect(button.textContent).toMatch(/how to upgrade/i)
    expect(target.textContent).not.toMatch(/upgrade now/i)
    button.click()
    await Promise.resolve()

    const dialog = target.querySelector('[role="dialog"]')
    expect(dialog).toBeTruthy()
    expect(dialog.textContent).toContain('Redeploy Soulacy')
    expect(dialog.textContent).toContain('Render')
    expect(dialog.textContent).toContain('ghcr.io/vmodekurti/soulacy-personal:1.2.3')
    expect(dialog.textContent).toContain('Manual Deploy')
    expect(dialog.textContent).toContain('v1.2.2')
    expect(dialog.textContent).toContain('v1.2.3')
    expect(onUpgrade).not.toHaveBeenCalled()

    const checkButton = [...dialog.querySelectorAll('button')]
      .find((candidate) => /redeployed.*check again/i.test(candidate.textContent || ''))
    expect(checkButton).toBeTruthy()
    checkButton.click()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onCheck).toHaveBeenCalledOnce()
    expect(dialog.textContent).toContain('Still running v1.2.2')
  })
})
