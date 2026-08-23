// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import PageHelp from './PageHelp.svelte'

let app
let target

afterEach(() => {
  app?.$destroy()
  target?.remove()
  app = null
  target = null
})

function render(props = {}) {
  target = document.createElement('div')
  document.body.appendChild(target)
  app = new PageHelp({ target, props })
}

describe('contextual page help', () => {
  it('explains the deployment administrator boundary and links to more help', async () => {
    render({ page: 'admin-login' })

    document.querySelector('.help-button').click()
    await Promise.resolve()

    expect(document.querySelector('[role="dialog"] h2')?.textContent).toBe('Deployment administrator login')
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('cannot read tenant agents')
    expect(document.querySelector('a[href="https://docs.soulacy.io/configuration/platform-administration/"]')).not.toBeNull()
    expect(document.querySelector('a[href="https://soulacy.io"]')).not.toBeNull()
  })

  it('uses page-specific diagnostics guidance', async () => {
    render({ page: 'platform-diagnostics', compact: true })

    expect(document.querySelector('.help-button')?.textContent).toContain('Help')
    document.querySelector('.help-button').click()
    await Promise.resolve()

    expect(document.querySelector('[role="dialog"] h2')?.textContent).toBe('Platform diagnostics')
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('redacted support snapshot')
  })
})
