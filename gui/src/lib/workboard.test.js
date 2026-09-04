import { describe, it, expect } from 'vitest'
import { STATUSES, STATUS_LABELS, adjacentStatus, groupByStatus, canRun, runLabel, artifactName, formatBytes, artifactDownloadUrl, PRIORITIES, priorityBadge, parseTags, formatTags, dueInfo } from './workboard.js'

describe('workboard column model', () => {
  it('has five columns in lifecycle order', () => {
    expect(STATUSES).toEqual(['todo', 'running', 'needs_review', 'done', 'failed'])
  })

  it('labels every status', () => {
    for (const s of STATUSES) expect(STATUS_LABELS[s]).toBeTruthy()
  })
})

describe('adjacentStatus', () => {
  it('moves right through the lifecycle', () => {
    expect(adjacentStatus('todo', +1)).toBe('running')
    expect(adjacentStatus('running', +1)).toBe('needs_review')
    expect(adjacentStatus('needs_review', +1)).toBe('done')
    expect(adjacentStatus('done', +1)).toBe('failed')
  })

  it('moves left through the lifecycle', () => {
    expect(adjacentStatus('running', -1)).toBe('todo')
    expect(adjacentStatus('failed', -1)).toBe('done')
  })

  it('returns null at the edges', () => {
    expect(adjacentStatus('todo', -1)).toBeNull()
    expect(adjacentStatus('failed', +1)).toBeNull()
  })

  it('returns null for unknown statuses', () => {
    expect(adjacentStatus('bogus', +1)).toBeNull()
    expect(adjacentStatus('', -1)).toBeNull()
  })
})

describe('canRun', () => {
  it('requires an assigned agent', () => {
    expect(canRun({ agent_id: '', status: 'todo' })).toBe(false)
    expect(canRun({ agent_id: 'bot-1', status: 'todo' })).toBe(true)
  })

  it('blocks tasks that are already running', () => {
    expect(canRun({ agent_id: 'bot-1', status: 'running' })).toBe(false)
  })

  it('allows retry from any terminal status', () => {
    for (const s of ['needs_review', 'done', 'failed']) {
      expect(canRun({ agent_id: 'bot-1', status: s })).toBe(true)
    }
  })

  it('tolerates null', () => {
    expect(canRun(null)).toBe(false)
  })
})

describe('runLabel', () => {
  it('says Retry for failed tasks, Run otherwise', () => {
    expect(runLabel({ status: 'failed' })).toBe('Retry')
    expect(runLabel({ status: 'todo' })).toBe('Run')
    expect(runLabel({ status: 'done' })).toBe('Run')
  })
})

describe('groupByStatus', () => {
  it('always returns every column', () => {
    const cols = groupByStatus([])
    expect(Object.keys(cols)).toEqual(STATUSES)
    for (const s of STATUSES) expect(cols[s]).toEqual([])
  })

  it('buckets tasks by status and drops unknown statuses', () => {
    const cols = groupByStatus([
      { id: 1, status: 'todo' },
      { id: 2, status: 'done' },
      { id: 3, status: 'todo' },
      { id: 4, status: 'bogus' },
    ])
    expect(cols.todo.map(t => t.id)).toEqual([1, 3])
    expect(cols.done.map(t => t.id)).toEqual([2])
    expect(cols.running).toEqual([])
  })

  it('tolerates null input', () => {
    expect(groupByStatus(null).todo).toEqual([])
  })
})

describe('artifact helpers (Story 13)', () => {
  it('artifactName takes the base name', () => {
    expect(artifactName('/tmp/reports/q4.pdf')).toBe('q4.pdf')
    expect(artifactName('plain.txt')).toBe('plain.txt')
    expect(artifactName('')).toBe('')
  })

  it('formatBytes scales units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(1500)).toBe('1.5 KB')
    expect(formatBytes(3400000)).toBe('3.4 MB')
    expect(formatBytes(5600000000)).toBe('5.6 GB')
    expect(formatBytes(undefined)).toBe('0 B')
  })

  it('artifactDownloadUrl appends the key only when present', () => {
    expect(artifactDownloadUrl(7)).toBe('/api/v1/workboard/artifacts/7/download')
    expect(artifactDownloadUrl(7, 'k&y')).toBe('/api/v1/workboard/artifacts/7/download?api_key=k%26y')
  })
})

describe('collaboration helpers (Story 14)', () => {
  it('priorityBadge marks non-normal priorities only', () => {
    expect(priorityBadge('normal')).toBe('')
    expect(priorityBadge('low')).toBe('▽')
    expect(priorityBadge('high')).toBe('▲')
    expect(priorityBadge('urgent')).toBe('‼')
  })

  it('parseTags normalises the editor input', () => {
    expect(parseTags(' Q4, finance ,,ops')).toEqual(['q4', 'finance', 'ops'])
    expect(parseTags('')).toEqual([])
    expect(parseTags(null)).toEqual([])
  })

  it('formatTags round-trips', () => {
    expect(formatTags(['q4', 'ops'])).toBe('q4, ops')
    expect(formatTags(null)).toBe('')
  })

  it('dueInfo flags overdue and labels near dates', () => {
    const now = new Date('2026-06-06T12:00:00Z')
    expect(dueInfo(null)).toBeNull()
    expect(dueInfo('2026-06-05T12:00:00Z', now).overdue).toBe(true)
    expect(dueInfo('2026-06-06T13:00:00Z', now)).toEqual({ label: 'due today', overdue: false })
    expect(dueInfo('2026-06-07T13:00:00Z', now).label).toBe('due tomorrow')
    expect(dueInfo('2026-06-20T12:00:00Z', now).overdue).toBe(false)
  })

  it('PRIORITIES escalate', () => {
    expect(PRIORITIES).toEqual(['low', 'normal', 'high', 'urgent'])
  })
})
