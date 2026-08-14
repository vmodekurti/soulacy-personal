import { describe, expect, it } from 'vitest'
import { normalizeYamlEditorText } from './yamltext.js'

describe('normalizeYamlEditorText', () => {
  it('collapses trailing blank rows to one terminal newline', () => {
    expect(normalizeYamlEditorText('id: agent\nname: Agent\n\n\n   \n'))
      .toBe('id: agent\nname: Agent\n')
  })

  it('does not alter blank lines inside the YAML document', () => {
    expect(normalizeYamlEditorText('id: agent\n\nname: Agent'))
      .toBe('id: agent\n\nname: Agent\n')
  })
})
