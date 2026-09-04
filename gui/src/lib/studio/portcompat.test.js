import { describe, it, expect } from 'vitest'
import { canonType, portsCompatible, portType, validateConnection } from './portcompat.js'

describe('canonType', () => {
  it('canonicalizes synonyms', () => {
    expect(canonType('Str')).toBe('string')
    expect(canonType('integer')).toBe('number')
    expect(canonType('object')).toBe('json')
    expect(canonType('boolean')).toBe('bool')
    expect(canonType('array')).toBe('list')
  })
  it('treats empty as untyped wildcard', () => {
    expect(canonType('')).toBe('')
    expect(canonType(undefined)).toBe('')
  })
  it('preserves unknown custom types by name', () => {
    expect(canonType('Notebook')).toBe('notebook')
  })
})

describe('portsCompatible', () => {
  it('wildcards (untyped/any/json) satisfy anything', () => {
    expect(portsCompatible('', 'number')).toBe(true)
    expect(portsCompatible('string', '')).toBe(true)
    expect(portsCompatible('json', 'number')).toBe(true)
    expect(portsCompatible('any', 'notebook')).toBe(true)
  })
  it('identical types match', () => {
    expect(portsCompatible('string', 'str')).toBe(true)
    expect(portsCompatible('Notebook', 'notebook')).toBe(true)
  })
  it('allows safe widenings into string', () => {
    expect(portsCompatible('number', 'string')).toBe(true)
    expect(portsCompatible('bool', 'text')).toBe(true)
  })
  it('rejects genuine mismatches', () => {
    expect(portsCompatible('string', 'number')).toBe(false)
    expect(portsCompatible('list', 'number')).toBe(false)
    expect(portsCompatible('notebook', 'string')).toBe(false)
  })
})

describe('portType', () => {
  const node = {
    id: 'a',
    inputs: [{ name: 'q', type: 'string' }, 'raw'],
    outputs: [{ name: 'id', type: 'string' }, { name: 'data', type: 'json' }],
  }
  it('reads declared types by handle + direction', () => {
    expect(portType(node, 'id', 'out')).toBe('string')
    expect(portType(node, 'data', 'out')).toBe('json')
    expect(portType(node, 'q', 'in')).toBe('string')
  })
  it('string-form ports are untyped', () => {
    expect(portType(node, 'raw', 'in')).toBe('')
  })
  it('missing port or node → untyped', () => {
    expect(portType(node, 'nope', 'in')).toBe('')
    expect(portType(null, 'q', 'in')).toBe('')
  })
})

describe('validateConnection', () => {
  const nodes = [
    { id: 'mk', outputs: [{ name: 'id', type: 'string' }, { name: 'doc', type: 'json' }] },
    { id: 'use', inputs: [{ name: 'notebook_id', type: 'string' }, { name: 'count', type: 'number' }] },
  ]
  it('allows a compatible typed wire', () => {
    const r = validateConnection({ nodes, source: 'mk', target: 'use', sourceHandle: 'id', targetHandle: 'notebook_id' })
    expect(r.ok).toBe(true)
  })
  it('rejects an incompatible typed wire with a reason', () => {
    const r = validateConnection({ nodes, source: 'mk', target: 'use', sourceHandle: 'id', targetHandle: 'count' })
    expect(r.ok).toBe(false)
    expect(r.reason).toMatch(/type mismatch/)
  })
  it('json output satisfies a typed input (universal handshake)', () => {
    const r = validateConnection({ nodes, source: 'mk', target: 'use', sourceHandle: 'doc', targetHandle: 'count' })
    expect(r.ok).toBe(true)
  })
  it('untyped/implicit handles are permissive', () => {
    const r = validateConnection({ nodes, source: 'mk', target: 'use' })
    expect(r.ok).toBe(true)
  })
  it('unknown nodes are not blocked here', () => {
    const r = validateConnection({ nodes, source: 'ghost', target: 'use', targetHandle: 'count' })
    expect(r.ok).toBe(true)
  })
})
