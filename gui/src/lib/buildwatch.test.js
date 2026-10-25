import { describe, it, expect } from 'vitest'
import {
  buildFromHealth, classifyChange, isFreshStart, agoLabel, commitLabel,
  buildDetailsText, createBuildWatch, LAST_SEEN_KEY,
} from './buildwatch.js'

const health = (label, id, instance, extra = {}) => ({
  status: 'ok',
  build: { label, build_id: id, instance_id: instance, version: 'v1.2.0', started_at: '2026-10-18T14:55:00Z', ...extra },
})

function memoryStorage(initial = {}) {
  const data = { ...initial }
  return {
    getItem: k => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v) },
    data,
  }
}

describe('buildFromHealth', () => {
  it('returns null for gateways that predate build labels', () => {
    expect(buildFromHealth({ status: 'ok', version: 'v0.9' })).toBeNull()
    expect(buildFromHealth(null)).toBeNull()
  })
  it('normalises the build record', () => {
    const b = buildFromHealth(health('amber-falcon', '3f9a2c1b4e07', 'aa', { modified: 1 }))
    expect(b.label).toBe('amber-falcon')
    expect(b.modified).toBe(true)
  })
})

describe('classifyChange', () => {
  const a = { build_id: 'x', instance_id: '1' }
  it('detects a new build, a restart, and no change', () => {
    expect(classifyChange(a, { build_id: 'y', instance_id: '2' })).toBe('updated')
    expect(classifyChange(a, { build_id: 'x', instance_id: '2' })).toBe('restarted')
    expect(classifyChange(a, { build_id: 'x', instance_id: '1' })).toBe('none')
    expect(classifyChange(null, a)).toBe('none')
  })
})

describe('labels', () => {
  const now = new Date('2026-10-18T15:00:00Z').getTime()
  it('flags a fresh start inside the window only', () => {
    expect(isFreshStart({ started_at: '2026-10-18T14:55:00Z' }, now)).toBe(true)
    expect(isFreshStart({ started_at: '2026-10-18T12:00:00Z' }, now)).toBe(false)
    expect(isFreshStart({}, now)).toBe(false)
  })
  it('formats relative times and commits', () => {
    expect(agoLabel('2026-10-18T14:55:00Z', now)).toBe('5 min ago')
    expect(agoLabel('2026-10-18T11:00:00Z', now)).toBe('4 h ago')
    expect(commitLabel({ commit: 'fc2fe43fe400', modified: true })).toBe('fc2fe43 + local changes')
    expect(commitLabel({})).toBe('not recorded')
  })
  it('renders copyable details', () => {
    const text = buildDetailsText(buildFromHealth(health('amber-falcon', '3f9a2c1b4e07', 'aa')), now)
    expect(text).toContain('Soulacy runtime build: amber-falcon (3f9a2c1b4e07)')
    expect(text).toContain('Instance: aa')
  })
})

describe('createBuildWatch', () => {
  it('announces a new build that appears while the page is open, with a reload', () => {
    const changes = []
    const w = createBuildWatch({ storage: memoryStorage(), onChange: c => changes.push(c) })
    w.observe(health('amber-falcon', 'aaaaaaaaaaaa', '1'))
    w.observe(health('amber-falcon', 'aaaaaaaaaaaa', '1'))
    expect(changes).toEqual([])
    w.observe(health('copper-otter', 'bbbbbbbbbbbb', '2'))
    expect(changes).toHaveLength(1)
    expect(changes[0]).toMatchObject({ kind: 'updated', reloadNeeded: true })
    expect(changes[0].from.label).toBe('amber-falcon')
    expect(changes[0].to.label).toBe('copper-otter')
  })

  it('reports a plain restart separately from an update', () => {
    const changes = []
    const w = createBuildWatch({ storage: memoryStorage(), onChange: c => changes.push(c) })
    w.observe(health('amber-falcon', 'aaaaaaaaaaaa', '1'))
    w.observe(health('amber-falcon', 'aaaaaaaaaaaa', '2'))
    expect(changes).toEqual([expect.objectContaining({ kind: 'restarted', reloadNeeded: false })])
  })

  it('announces once after a reload onto a new build, without asking to reload again', () => {
    const storage = memoryStorage({ [LAST_SEEN_KEY]: JSON.stringify({ label: 'amber-falcon', build_id: 'aaaaaaaaaaaa', instance_id: '1' }) })
    const changes = []
    createBuildWatch({ storage, onChange: c => changes.push(c) }).observe(health('copper-otter', 'bbbbbbbbbbbb', '2'))
    expect(changes).toEqual([expect.objectContaining({ kind: 'updated', reloadNeeded: false })])
    expect(JSON.parse(storage.data[LAST_SEEN_KEY]).label).toBe('copper-otter')

    const again = []
    createBuildWatch({ storage, onChange: c => again.push(c) }).observe(health('copper-otter', 'bbbbbbbbbbbb', '2'))
    expect(again).toEqual([])
  })

  it('survives storage that throws', () => {
    const broken = { getItem() { throw new Error('blocked') }, setItem() { throw new Error('blocked') } }
    const builds = []
    createBuildWatch({ storage: broken, onBuild: b => builds.push(b) }).observe(health('amber-falcon', 'aaaaaaaaaaaa', '1'))
    expect(builds).toHaveLength(1)
  })
})
