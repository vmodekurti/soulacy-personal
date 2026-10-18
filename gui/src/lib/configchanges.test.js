import { describe, it, expect } from 'vitest'
import { changedSettings } from './configchanges.js'

describe('configuration edits', () => {
  it('sends whole plugin values because the server merges settings by key', () => {
    const before = { plugins_config: { 'plugin.example': { nested: { a: 1, b: 2 }, secret: '***' } } }
    const after = { plugins_config: { 'plugin.example': { nested: { a: 3, b: 2 }, secret: '***' } } }
    expect(changedSettings(before, after)).toEqual({ plugins_config: { 'plugin.example': { nested: { a: 3, b: 2 } } } })
  })
  it('retains explicit deletes and empty lists', () => {
    expect(changedSettings({ plugins_config: { p: { old: 'x' } }, skill_dirs: ['a'] }, { plugins_config: { p: { old: null } }, skill_dirs: [] })).toEqual({ plugins_config: { p: { old: null } }, skill_dirs: [] })
  })
})
