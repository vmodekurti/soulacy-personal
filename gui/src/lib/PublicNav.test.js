// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import PublicNav from './PublicNav.svelte'

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
  app = new PublicNav({ target, props })
}

describe('public access navigation', () => {
  it('keeps product, login, and marketing destinations available', () => {
    render({ current: 'admin' })

    expect(document.querySelector('a[href="/"]')?.textContent).toContain('Soulacy')
    expect([...document.querySelectorAll('nav a')].map((link) => link.textContent.trim())).toEqual([
      'Home',
      'Workspace login',
      'Deployment admin login',
      'Soulacy.io ↗',
    ])
    expect(document.querySelector('a[href="/admin"]')?.getAttribute('aria-current')).toBe('page')
    expect(document.querySelector('a[href="/workspace-login"]')).not.toBeNull()
    expect(document.querySelector('a[href="https://soulacy.io"]')?.getAttribute('target')).toBe('_blank')
    expect(document.querySelector('a[href="https://soulacy.io"]')?.getAttribute('rel')).toBe('noreferrer')
  })
})
