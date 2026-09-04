import { describe, it, expect } from 'vitest'
import {
  PYTHON_TEMPLATES, BLANK_PYTHON, templateByKey, pythonCodeFor, pythonLabelFor,
} from './pythonTemplates.js'

describe('pythonTemplates', () => {
  it('exposes the five named templates from the spec', () => {
    const keys = PYTHON_TEMPLATES.map((t) => t.key)
    expect(keys).toEqual(['clean_csv', 'transform_json', 'calculate_metrics', 'validate_records', 'chart_data'])
  })
  it('every template carries a label, why, and a def run(inputs) body', () => {
    for (const t of PYTHON_TEMPLATES) {
      expect(t.label).toBeTruthy()
      expect(t.why).toMatch(/Python is used here/)
      expect(t.code).toMatch(/def run\(inputs\)/)
    }
  })
  it('templateByKey resolves and misses cleanly', () => {
    expect(templateByKey('clean_csv').label).toBe('Clean CSV')
    expect(templateByKey('nope')).toBeNull()
  })
  it('pythonCodeFor falls back to the blank starter', () => {
    expect(pythonCodeFor('transform_json')).toMatch(/Transform JSON/)
    expect(pythonCodeFor('nope')).toBe(BLANK_PYTHON)
    expect(pythonCodeFor()).toBe(BLANK_PYTHON)
  })
  it('pythonLabelFor returns a friendly label or a generic default', () => {
    expect(pythonLabelFor('chart_data')).toBe('Generate Chart Data')
    expect(pythonLabelFor('nope')).toBe('Custom Python')
  })
})
