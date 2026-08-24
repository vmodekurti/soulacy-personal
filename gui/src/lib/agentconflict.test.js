// MU-028 criterion 3 — the choices the GUI offers when a save loses a race,
// and the property that makes "overwrite" a choice rather than a relapse.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  conflictFrom, conflictHeadline, overwriteOptions, diffLines, conflictSummary,
} from './agentconflict.js'
import {
  resourceKey, rememberVersion, rememberVersions, versionFor, forgetVersion,
  clearVersions, isConflict, isPreconditionRequired,
} from './resourceversions.js'
import { api, apiFetch } from './api.js'

function staleError(extra = {}) {
  return Object.assign(new Error('“support-bot” was changed by usr_bob since you loaded it.'), {
    status: 409,
    body: {
      code: 'stale_resource_version',
      error: '“support-bot” was changed by usr_bob since you loaded it.',
      current_version: '"theirs"',
      supplied_version: '"mine"',
      last_modified_by: 'usr_bob',
      last_modified_at: '2026-08-18T10:00:00Z',
      ...extra,
    },
  })
}

beforeEach(() => clearVersions())

describe('the three offers', () => {
  it('recognises a stale save and keeps what the server said about it', () => {
    const c = conflictFrom(staleError(), { agentId: 'support-bot', mine: 'a' })
    expect(c.kind).toBe('stale')
    expect(c.lastModifiedBy).toBe('usr_bob')
    expect(c.currentVersion).toBe('"theirs"')
    // The echo is what lets a user with several edits in flight tell which lost.
    expect(c.suppliedVersion).toBe('"mine"')
  })

  it('distinguishes "your version is stale" from "you sent none"', () => {
    const missing = Object.assign(new Error('needs version'), {
      status: 428, body: { code: 'stale_resource_version' },
    })
    expect(conflictFrom(missing, {}).kind).toBe('precondition')
    // The remedies differ, so the sentences must: one asks the user to choose,
    // the other just tells them to reload.
    expect(conflictHeadline(conflictFrom(missing, {}))).toMatch(/out of date/i)
  })

  it('passes non-concurrency errors straight through', () => {
    const other = Object.assign(new Error('nope'), { status: 409, body: { needs_ack: true } })
    expect(conflictFrom(other, {})).toBeNull()
    expect(conflictFrom(Object.assign(new Error('x'), { status: 500, body: {} }), {})).toBeNull()
  })

  it('names the other actor, because the remedy is a conversation', () => {
    const headline = conflictHeadline(conflictFrom(staleError(), { agentId: 'support-bot' }))
    expect(headline).toContain('usr_bob')
    expect(headline).toContain('support-bot')
  })

  it('degrades to an unattributed sentence rather than naming nobody', () => {
    const c = conflictFrom(staleError({ last_modified_by: '' }), { agentId: 'support-bot' })
    const headline = conflictHeadline(c)
    expect(headline).toContain('was changed')
    expect(headline).not.toMatch(/by\s+(undefined|null|\s*$)/)
  })
})

// THE PROPERTY THAT MATTERS. Overwrite must stay conditional. If it dropped
// the precondition, a third save landing between the 409 and the overwrite
// would be discarded silently — the original bug, reached by a different route
// and with the user's explicit blessing on the wrong thing.
describe('overwrite is deliberate, not unconditional', () => {
  it('resends the version the server named', () => {
    const opts = overwriteOptions(conflictFrom(staleError(), {}))
    expect(opts.headers['If-Match']).toBe('"theirs"')
  })

  it('refuses to build an overwrite with no version to name', () => {
    const c = conflictFrom(staleError({ current_version: '', etag: '' }), {})
    expect(overwriteOptions(c)).toBeNull()
  })
})

describe('compare shows what would be lost', () => {
  it('marks each side of a divergence', () => {
    const lines = diffLines('id: bot\nname: Mine\nenabled: true', 'id: bot\nname: Theirs\nenabled: true')
    expect(lines.filter(l => l.kind === 'mine').map(l => l.text)).toEqual(['name: Mine'])
    expect(lines.filter(l => l.kind === 'theirs').map(l => l.text)).toEqual(['name: Theirs'])
    expect(lines.filter(l => l.kind === 'same').map(l => l.text)).toEqual(['id: bot', 'enabled: true'])
  })

  it('reports identical content as identical', () => {
    expect(conflictSummary('a\nb', 'a\nb')).toEqual({ yours: 0, theirs: 0, identical: true })
  })

  it('counts both directions', () => {
    expect(conflictSummary('a\nb\nc', 'a\nx\ny\nc')).toEqual({ yours: 1, theirs: 2, identical: false })
  })
})

// The version registry is the reason a page cannot forget to send a
// precondition. These pin the parts a refactor would plausibly break.
describe('the version registry', () => {
  it('treats every way of writing one agent as one resource', () => {
    // The server derives ONE validator from the definition, so a version read
    // through the form view must satisfy a save through the code view.
    expect(resourceKey('/agents/support-bot')).toBe('agents/support-bot')
    expect(resourceKey('/agents/support-bot/yaml')).toBe('agents/support-bot')
    expect(resourceKey('/agents/support-bot/rollback')).toBe('agents/support-bot')
    expect(resourceKey('/studio/agents/support-bot')).toBe('agents/support-bot')
    rememberVersion('/agents/support-bot/yaml', '"v1"')
    expect(versionFor('/agents/support-bot')).toBe('"v1"')
  })

  it('stays out of the way of paths that are not an agent', () => {
    expect(resourceKey('/agents')).toBe('')
    expect(resourceKey('/agents/validate')).toBe('')
    expect(resourceKey('/agents/package/import')).toBe('')
    expect(resourceKey('/costs/estimate')).toBe('')
  })

  it('takes versions from the list, which is the only place the GUI reads agents', () => {
    rememberVersions({ 'support-bot': '"v9"', 'other': '"v3"' })
    expect(versionFor('/agents/support-bot')).toBe('"v9"')
    expect(versionFor('/agents/other/yaml')).toBe('"v3"')
  })

  it('drops a version the server has just refused', () => {
    rememberVersion('/agents/support-bot', '"v1"')
    forgetVersion('/agents/support-bot')
    expect(versionFor('/agents/support-bot')).toBe('')
  })

  it('classifies only the concurrency refusals', () => {
    expect(isConflict(staleError())).toBe(true)
    expect(isConflict(Object.assign(new Error('x'), { status: 409, body: { needs_ack: true } }))).toBe(false)
    expect(isPreconditionRequired(Object.assign(new Error('x'), {
      status: 428, body: { code: 'stale_resource_version' },
    }))).toBe(true)
  })
})

// The transport half. Asserted through apiFetch rather than by calling the
// helper directly: the helper working proves nothing about whether any request
// goes through it, and "tested the helper, not the call site" is exactly how a
// guard passes for the wrong reason.
describe('apiFetch attaches the precondition itself', () => {
  beforeEach(() => {
    clearVersions()
    globalThis.fetch = vi.fn()
  })

  function response(status, body = {}, headers = {}) {
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: `HTTP ${status}`,
      headers: { get: (name) => headers[name] || null },
      json: async () => body,
      text: async () => JSON.stringify(body),
    }
  }

  it('sends the version a previous read handed back, without the caller asking', async () => {
    fetch.mockResolvedValueOnce(response(200, { id: 'support-bot' }, { ETag: '"v1"' }))
    await apiFetch('/agents/support-bot')

    fetch.mockResolvedValueOnce(response(200, { ok: true }))
    await apiFetch('/agents/support-bot', { method: 'PUT', body: '{}' })

    const sent = fetch.mock.calls[1][1].headers
    expect(sent['If-Match']).toBe('"v1"')
  })

  it('records the per-agent versions the list carries', async () => {
    fetch.mockResolvedValueOnce(response(200, { agents: [], versions: { 'support-bot': '"v7"' } }))
    await apiFetch('/agents')

    fetch.mockResolvedValueOnce(response(200, { ok: true }))
    await apiFetch('/agents/support-bot', { method: 'DELETE' })
    expect(fetch.mock.calls[1][1].headers['If-Match']).toBe('"v7"')
  })

  it('uses a canonical version path for composite Studio saves', async () => {
    rememberVersion('/studio/agents/support-bot', '"v7"')
    fetch.mockResolvedValueOnce(response(201, { agentId: 'support-bot' }, { ETag: '"v8"' }))
    await api.studio.save({ workflow: { id: 'support-bot', name: 'Support bot' } })
    expect(fetch.mock.calls[0][1].headers['If-Match']).toBe('"v7"')
    expect(versionFor('/agents/support-bot')).toBe('"v8"')
  })

  it('uses the loaded agent version when Studio saves authoritative YAML', async () => {
    rememberVersion('/agents/support-bot', '"v4"')
    fetch.mockResolvedValueOnce(response(200, { id: 'support-bot' }, { ETag: '"v5"' }))
    await api.studio.saveYaml({ yaml: 'id: support-bot\n', agentId: 'support-bot' })
    expect(fetch.mock.calls[0][1].headers['If-Match']).toBe('"v4"')
    expect(versionFor('/studio/agents/support-bot')).toBe('"v5"')
  })

  it('never sends a precondition on a read', async () => {
    fetch.mockResolvedValueOnce(response(200, { agents: [], versions: { 'support-bot': '"v7"' } }))
    await apiFetch('/agents')
    fetch.mockResolvedValueOnce(response(200, {}))
    await apiFetch('/agents/support-bot')
    expect(fetch.mock.calls[1][1].headers['If-Match']).toBeUndefined()
  })

  it('lets an explicit overwrite win over the remembered version', async () => {
    fetch.mockResolvedValueOnce(response(200, { agents: [], versions: { 'support-bot': '"stale"' } }))
    await apiFetch('/agents')

    fetch.mockResolvedValueOnce(response(200, { ok: true }))
    await apiFetch('/agents/support-bot', {
      method: 'PUT', body: '{}', headers: { 'If-Match': '"theirs"' },
    })
    expect(fetch.mock.calls[1][1].headers['If-Match']).toBe('"theirs"')
  })

  it('forgets a version the server refused, so the next save cannot repeat it', async () => {
    fetch.mockResolvedValueOnce(response(200, { agents: [], versions: { 'support-bot': '"v1"' } }))
    await apiFetch('/agents')

    fetch.mockResolvedValueOnce(response(409, { code: 'stale_resource_version', current_version: '"v2"' }))
    await expect(apiFetch('/agents/support-bot', { method: 'PUT', body: '{}' })).rejects.toMatchObject({ status: 409 })

    fetch.mockResolvedValueOnce(response(200, { ok: true }))
    await apiFetch('/agents/support-bot', { method: 'PUT', body: '{}' })
    expect(fetch.mock.calls[2][1].headers['If-Match']).toBeUndefined()
  })
})
