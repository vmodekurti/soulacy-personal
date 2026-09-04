import { describe, it, expect } from 'vitest'
import { channelGuides, guideFor, renderInline } from './channelguides.js'

// Must cover every channel the gateway catalogs (internal/gateway/api.go
// channelSpecs) — extend both together.
const GATEWAY_CHANNELS = ['http', 'telegram', 'discord', 'slack', 'whatsapp', 'whatsapp_web']

describe('channelGuides', () => {
  it('covers every gateway channel', () => {
    for (const id of GATEWAY_CHANNELS) {
      expect(guideFor(id), id).toBeTruthy()
    }
  })

  it('every guide has intro, steps, and a test tip', () => {
    for (const [id, g] of Object.entries(channelGuides)) {
      expect(g.intro, id).toBeTruthy()
      expect(Array.isArray(g.steps) && g.steps.length > 0, id).toBe(true)
      expect(g.test, id).toBeTruthy()
      expect(typeof g.fields, id).toBe('object')
    }
  })

  it('unknown channels return null', () => {
    expect(guideFor('matrix')).toBeNull()
  })
})

describe('renderInline', () => {
  it('renders bold and code, escapes HTML', () => {
    expect(renderInline('go to **Bot** tab and run `npm i`'))
      .toBe('go to <strong>Bot</strong> tab and run <code>npm i</code>')
    expect(renderInline('<script>alert(1)</script>'))
      .toBe('&lt;script&gt;alert(1)&lt;/script&gt;')
  })
})
