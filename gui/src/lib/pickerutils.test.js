import { describe, it, expect } from 'vitest'
import { filterPickerOptions, excludeSelected } from './pickerutils.js'

const opts = [
  { value: '~/.soulacy/tools/ai_daily_pipeline.py', label: 'ai_daily_pipeline', description: 'Daily AI podcast pipeline' },
  { value: '~/.soulacy/tools/scrape.py', label: 'scrape', description: 'Web scraper' },
  { value: 'telegram', label: 'telegram' },
]

describe('filterPickerOptions', () => {
  it('returns everything for an empty query', () => {
    expect(filterPickerOptions(opts, '')).toHaveLength(3)
    expect(filterPickerOptions(opts, '   ')).toHaveLength(3)
  })
  it('matches label case-insensitively', () => {
    expect(filterPickerOptions(opts, 'SCRAPE')).toEqual([opts[1]])
  })
  it('matches value (paths)', () => {
    expect(filterPickerOptions(opts, 'tools/ai_daily')).toEqual([opts[0]])
  })
  it('matches description', () => {
    expect(filterPickerOptions(opts, 'podcast')).toEqual([opts[0]])
  })
  it('no hits → empty array', () => {
    expect(filterPickerOptions(opts, 'zzz-nothing')).toEqual([])
  })
  it('tolerates missing fields and non-array input', () => {
    expect(filterPickerOptions(null, 'x')).toEqual([])
    expect(filterPickerOptions([{ value: 'v' }], 'v')).toHaveLength(1)
  })
})

describe('excludeSelected', () => {
  it('drops already-selected values', () => {
    expect(excludeSelected(opts, ['telegram']).map(o => o.value))
      .toEqual([opts[0].value, opts[1].value])
  })
  it('handles empty/undefined selection', () => {
    expect(excludeSelected(opts, undefined)).toHaveLength(3)
  })
})
