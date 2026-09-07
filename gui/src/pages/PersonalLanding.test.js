import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const landing = readFileSync(fileURLToPath(new URL('./PersonalLanding.svelte', import.meta.url)), 'utf8')
const app = readFileSync(fileURLToPath(new URL('../App.svelte', import.meta.url)), 'utf8')

describe('Personal public landing page', () => {
  it('positions login in the header, hero, and closing call to action', () => {
    expect(landing.match(/href="#personal-login"/g)).toHaveLength(3)
    expect(landing).toContain('id="personal-login"')
    expect(landing).toContain('Log in securely')
    expect(landing).toContain('This browser stays trusted for up to 30 days')
  })

  it('communicates the Personal edition boundary and open-source model', () => {
    expect(landing).toContain('OPEN-SOURCE · SELF-HOSTED · PERSONAL AI')
    expect(landing).toContain('Your machine. Your rules.')
    expect(landing).toContain('https://github.com/vmodekurti/soulacy-personal')
    expect(landing).toContain('Bring your models')
  })

  it('is wired into the existing authenticated session exchange', () => {
    expect(app).toContain("import PersonalLanding from './pages/PersonalLanding.svelte'")
    expect(app).toContain('<PersonalLanding')
    expect(app).toContain('on:login={submitLogin}')
    expect(app).toContain('const session = await api.auth.login(key)')
  })

  it('waits for session discovery and remounts the active page after login', () => {
    expect(app).toContain('let sessionChecked = false')
    expect(app).toContain("sessionChecked && !$authRequired && page !== loadedPage")
    expect(app).toContain('Restoring your session…')
    expect(app).toContain("loadedPage = ''")
    expect(app).toContain('sessionChecked = true')
  })

  it('keeps touch targets and a single-column mobile layout', () => {
    expect(landing).toContain('min-height: 44px')
    expect(landing).toContain('.feature-grid { grid-template-columns: 1fr;')
    expect(landing).toContain('env(safe-area-inset-top)')
    expect(landing).toContain('font-size: 16px !important')
  })
})
