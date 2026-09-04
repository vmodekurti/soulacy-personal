import { describe, expect, it } from 'vitest'
import { createEditionRegistry, editionCapabilities } from './edition.js'
import { editionHas, resolveEdition } from '../distribution/edition.js'

describe('edition registry', () => {
  it('defaults unknown and empty modes to Personal', () => {
    expect(resolveEdition('').id).toBe('personal')
    expect(resolveEdition('future').id).toBe('personal')
  })

  it('inherits capabilities through the commercial edition chain', () => {
    expect(editionHas('team', editionCapabilities.multiUser)).toBe(true)
    expect(editionHas('scale', editionCapabilities.workspaceAdmin)).toBe(true)
    expect(editionHas('personal', editionCapabilities.multiUser)).toBe(false)
  })

  it('refuses replacement and missing parents', () => {
    expect(() => createEditionRegistry([{ id: 'personal', displayName: 'Replacement' }])).toThrow(/already registered/)
    expect(() => createEditionRegistry([{ id: 'enterprise', displayName: 'Enterprise', extends: 'missing' }])).toThrow(/unknown edition/)
  })
})
