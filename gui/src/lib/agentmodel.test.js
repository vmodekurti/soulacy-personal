import { describe, it, expect } from 'vitest'
import { modelAvailability } from './agentmodel.js'

describe('modelAvailability', () => {
  it('warns when no provider is selected', () => {
    expect(modelAvailability({ model: 'gpt-4o' })).toMatchObject({
      kind: 'warn',
      label: 'Provider missing',
    })
  })

  it('reports loading state before errors or list checks', () => {
    expect(modelAvailability({ provider: 'openai', model: 'gpt-4o', loading: true })).toMatchObject({
      kind: 'info',
      label: 'Checking models',
    })
  })

  it('reports provider list failures', () => {
    expect(modelAvailability({ provider: 'openroute', model: 'gemini', error: '502' })).toMatchObject({
      kind: 'warn',
      label: 'Model list unavailable',
    })
  })

  it('accepts a listed model', () => {
    expect(modelAvailability({ provider: 'google', model: 'gemini-2.5-pro', models: ['gemini-2.5-pro'] })).toMatchObject({
      kind: 'ok',
      label: 'Model available',
    })
  })

  it('flags an unlisted model without blocking custom aliases', () => {
    expect(modelAvailability({ provider: 'anthropic', model: 'claude-sonnet-5', models: ['claude-3-5-sonnet-latest'] })).toMatchObject({
      kind: 'warn',
      label: 'Unlisted model',
    })
  })
})
