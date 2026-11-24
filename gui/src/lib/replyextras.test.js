import { describe, it, expect } from 'vitest'
import { artifactKind, artifactSubtitle, artifactTitle, followUpsFor, formatBytes } from './replyextras.js'

describe('artifact cards', () => {
  it('names a file the way a person would', () => {
    expect(artifactTitle('weekly-preparation_brief.md')).toBe('Weekly preparation brief')
    expect(artifactTitle('out/reports/Q3 summary.pdf')).toBe('Q3 summary')
    expect(artifactTitle('')).toBe('Untitled file')
  })
  it('says what kind of thing it is', () => {
    expect(artifactKind('brief.md')).toBe('Document')
    expect(artifactKind('budget.xlsx')).toBe('Spreadsheet')
    expect(artifactKind('episode.mp3')).toBe('Audio')
    expect(artifactKind('mystery.bin')).toBe('File')
  })
  it('builds the second line from what is known, skipping what is not', () => {
    expect(artifactSubtitle({ name: 'brief.md', size_bytes: 2048 })).toBe('Document · 2.0 KB · Ready to open')
    expect(artifactSubtitle({ name: 'brief.md' })).toBe('Document · Ready to open')
    expect(formatBytes(0)).toBe('')
  })
})

describe('followUpsFor', () => {
  const labels = (text, opts) => followUpsFor(text, opts).map((s) => s.label)
  const list = 'Here is where I would focus.\n\n1. **Governance**\n   Finalize decision rights.\n2. **Strategy**\n   Choose one idea.\n3. **Personal**\n   Plan travel.'

  it('offers a plan for a numbered list', () => {
    expect(labels(list)).toEqual(['Turn this into a plan'])
  })
  it('offers prioritizing for a bulleted list', () => {
    expect(labels('Options:\n- one thing\n- another thing\n- a third thing')).toEqual(['Help me prioritize'])
  })
  it('offers the file when the turn produced one', () => {
    expect(labels(list, { hasFiles: true })).toEqual(['Turn this into a plan', 'Walk me through the file'])
  })
  it('offers a shorter version of a long answer', () => {
    expect(labels('word '.repeat(300))).toEqual(['Give me the short version'])
  })
  it('offers nothing when the reply asks the person a question', () => {
    expect(labels(list + '\n\nWhich of these should I start on?')).toEqual([])
  })
  it('offers nothing for a short plain answer', () => {
    expect(labels('It is 4pm in Chicago.')).toEqual([])
    expect(labels('')).toEqual([])
  })
  it('does not count list markers inside code blocks', () => {
    expect(labels('Run this:\n```\n1. a\n2. b\n3. c\n```\nDone.')).toEqual([])
  })
  it('sends a full sentence, not the chip label', () => {
    expect(followUpsFor(list)[0].prompt).toMatch(/step-by-step plan/)
  })
})
