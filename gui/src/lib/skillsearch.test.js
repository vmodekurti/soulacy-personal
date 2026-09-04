import { describe, it, expect } from 'vitest'
import { searchSkills, parseSlashQuery, applySkillChoice } from './skillsearch.js'

const skills = [
  { name: 'pdf', description: 'Read and write PDF files' },
  { name: 'xlsx', description: 'Spreadsheets. Can also export to pdf.' },
  { name: 'pdf-merge', description: 'Combine documents' },
  { name: 'weather', description: 'Forecasts' },
]

describe('searchSkills', () => {
  it('returns everything, alphabetically, for an empty query', () => {
    expect(searchSkills(skills, '').map(s => s.name))
      .toEqual(['pdf', 'pdf-merge', 'weather', 'xlsx'])
  })

  // The whole reason this helper exists. A plain filter would leave "xlsx"
  // (whose description mentions pdf) sitting above the actual "pdf" skill.
  it('ranks an exact name match above a description match', () => {
    const got = searchSkills(skills, 'pdf').map(s => s.name)
    expect(got[0]).toBe('pdf')
    expect(got).toEqual(['pdf', 'pdf-merge', 'xlsx'])
  })

  it('ranks a name prefix above a mid-name match', () => {
    const list = [
      { name: 'export-csv', description: '' },
      { name: 'csv', description: '' },
      { name: 'csv-clean', description: '' },
    ]
    expect(searchSkills(list, 'csv').map(s => s.name))
      .toEqual(['csv', 'csv-clean', 'export-csv'])
  })

  it('finds a skill by description when the name does not match', () => {
    expect(searchSkills(skills, 'forecast').map(s => s.name)).toEqual(['weather'])
  })

  it('is case-insensitive and ignores surrounding whitespace', () => {
    expect(searchSkills(skills, '  PDF  ')[0].name).toBe('pdf')
  })

  it('returns an empty list when nothing matches', () => {
    expect(searchSkills(skills, 'zzzz')).toEqual([])
  })

  it('honours a limit', () => {
    expect(searchSkills(skills, '', { limit: 2 })).toHaveLength(2)
  })

  it('survives junk input rather than throwing', () => {
    expect(searchSkills(null, 'pdf')).toEqual([])
    expect(searchSkills(undefined, undefined)).toEqual([])
    expect(searchSkills([{}], 'x')).toEqual([])
  })

  // Ordering must not shift between keystrokes, or arrow-key selection jumps.
  it('is stable for equally-ranked matches', () => {
    const a = searchSkills(skills, 'pdf').map(s => s.name)
    const b = searchSkills([...skills].reverse(), 'pdf').map(s => s.name)
    expect(a).toEqual(b)
  })
})

describe('parseSlashQuery', () => {
  it('opens on a leading slash', () => {
    expect(parseSlashQuery('/pd', 3)).toBe('pd')
    expect(parseSlashQuery('/', 1)).toBe('')
  })

  it('does not open for a slash mid-sentence', () => {
    // Otherwise the picker ambushes anyone typing a URL, a path, or a date.
    expect(parseSlashQuery('see docs/config for details', 26)).toBeNull()
    expect(parseSlashQuery('ratio is 3/4', 12)).toBeNull()
  })

  it('closes once the user types a space and starts writing the message', () => {
    expect(parseSlashQuery('/pdf summarise this', 19)).toBeNull()
  })

  it('tracks the caret, not just the end of the text', () => {
    expect(parseSlashQuery('/pdf summarise', 3)).toBe('pd')
  })
})

describe('applySkillChoice', () => {
  it('replaces the partial token and leaves a trailing space to type into', () => {
    expect(applySkillChoice('/pd', 'pdf')).toBe('/pdf ')
  })

  it('preserves the rest of the message', () => {
    expect(applySkillChoice('/pd summarise this', 'pdf')).toBe('/pdf summarise this')
  })

  it('works from a bare slash', () => {
    expect(applySkillChoice('/', 'weather')).toBe('/weather ')
  })
})
