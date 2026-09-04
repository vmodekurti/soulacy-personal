// The remediation seam: Go declares the vocabulary, the client handles it.
//
// This contract spans two languages with nothing between them, and it has
// failed twice here. Studio.svelte's readinessAction shipped handling five
// actions the server never emitted while six the server did emit fell through
// to a no-op — a "Fix this" button that did nothing. The security panel later
// grew a second, parallel vocabulary. Both times each side was internally
// consistent and nothing compared them.
//
// So this reads both files. It is deliberately blunt: regex over source text.
// A parser would be more elegant and would not have caught anything more.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { handledActionIds, actionKind, applyDraftFix, DRAFT_FIXES, SHARED_CHANNELS } from './fixactions.js'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../../..')
const read = (p) => readFileSync(resolve(repoRoot, p), 'utf8')

const goVocabulary = read('internal/studio/fixactions.go')
const goSecurity = read('internal/studio/security_preflight.go')

/** Ids and kinds declared in the Go registry table. */
function goActions() {
  const table = goVocabulary.slice(goVocabulary.indexOf('var fixActions = []FixAction{'))
  const body = table.slice(0, table.indexOf('\n}'))
  const consts = Object.fromEntries(
    [...goVocabulary.matchAll(/\t(Fix\w+)\s+=\s+"([^"]+)"/g)].map((m) => [m[1], m[2]]),
  )
  return [...body.matchAll(/\{(Fix\w+),\s*"([^"]*)",\s*(FixKind\w+)\}/g)].map((m) => ({
    id: consts[m[1]],
    label: m[2],
    kind: { FixKindNavigate: 'navigate', FixKindApply: 'apply', FixKindFocus: 'focus' }[m[3]],
  }))
}

describe('fix-action vocabulary parity', () => {
  it('reads a non-trivial vocabulary out of Go', () => {
    const actions = goActions()
    expect(actions.length).toBeGreaterThan(5)
    for (const a of actions) {
      expect(a.id, `an entry failed to resolve its constant: ${JSON.stringify(a)}`).toBeTruthy()
      expect(a.kind).toBeTruthy()
    }
  })

  it('handles every action the server can emit', () => {
    const missing = goActions().map((a) => a.id).filter((id) => !handledActionIds().includes(id))
    expect(missing, `Go declares these but the client has no handler: ${missing.join(', ')}`).toEqual([])
  })

  it('handles nothing the server never declares', () => {
    const declared = goActions().map((a) => a.id)
    const stray = handledActionIds().filter((id) => !declared.includes(id))
    expect(stray, `the client handles ids Go does not declare: ${stray.join(', ')}`).toEqual([])
  })

  it('agrees with Go on what each action DOES', () => {
    // A navigate action wired as a draft edit (or vice versa) is worse than an
    // unhandled one: it silently does the wrong thing.
    const wrong = goActions().filter((a) => actionKind(a.id) !== a.kind)
      .map((a) => `${a.id}: Go says ${a.kind}, client says ${actionKind(a.id) || 'unknown'}`)
    expect(wrong, wrong.join('; ')).toEqual([])
  })

  it('gives every action button text', () => {
    const unlabelled = goActions().filter((a) => !a.label.trim()).map((a) => a.id)
    expect(unlabelled, `an action with no label renders no button: ${unlabelled.join(', ')}`).toEqual([])
  })

  it('keeps the security findings on the shared vocabulary', () => {
    // The security panel used to declare its own ids. Aliases are fine; a
    // second independent list is not.
    expect(goSecurity).not.toMatch(/SecurityFix\w+\s+=\s+"/)
  })
})

describe('applying a draft fix', () => {
  it('drops shared channels and keeps the agent reachable', () => {
    const { draft, message } = applyDraftFix({ channels: ['telegram', 'slack', 'http'] }, 'restrict_to_internal_channels')
    expect(draft.channels).toEqual(['http'])
    expect(message).toContain('telegram')
  })

  it('adds http when removing the shared channels would leave nothing', () => {
    const { draft } = applyDraftFix({ channels: ['telegram'] }, 'restrict_to_internal_channels')
    expect(draft.channels).toEqual(['http'])
  })

  it('reports a no-op instead of pretending to change something', () => {
    const res = applyDraftFix({ channels: ['http'] }, 'restrict_to_internal_channels')
    expect(res.draft).toBeUndefined()
    expect(res.message).toMatch(/already/i)
  })

  it('sets the intent gate without discarding other security settings', () => {
    const { draft } = applyDraftFix({ security: { passphrase: 'hunter2' } }, 'set_intent_gate_deny')
    expect(draft.security).toEqual({ passphrase: 'hunter2', intent_gate: 'deny' })
  })

  it('never mutates the draft it was given', () => {
    const original = { channels: ['telegram', 'http'], security: {} }
    const snapshot = JSON.stringify(original)
    applyDraftFix(original, 'restrict_to_internal_channels')
    applyDraftFix(original, 'set_intent_gate_deny')
    expect(JSON.stringify(original)).toBe(snapshot)
  })

  it('throws on an unknown action rather than doing nothing', () => {
    expect(() => applyDraftFix({}, 'set_something_else')).toThrow(/no draft fix/)
  })

  it('covers every shared channel the security review can name', () => {
    // Read the Go map itself. The first version of this test used a hand-typed
    // alternation of channel names, which passed while the client list actually
    // said "googlechat" for google_chat and omitted sms — so the fix would have
    // left both wired up while reporting the agent as internal-only.
    const goMap = read('internal/studio/security_preflight.go')
    const block = goMap.slice(goMap.indexOf('var studioSharedExternalChannels = map[string]bool{'))
    const names = [...block.slice(0, block.indexOf('\n}')).matchAll(/"([a-z_]+)":\s*true/g)].map((m) => m[1])
    expect(names.length, 'failed to read the Go channel list').toBeGreaterThan(5)
    for (const c of names) {
      expect(SHARED_CHANNELS, `security review knows "${c}" but the fix does not`).toContain(c)
    }
    for (const c of SHARED_CHANNELS) {
      expect(names, `the fix strips "${c}" but the security review does not consider it shared`).toContain(c)
    }
  })

  it('exposes each fix as a plain function', () => {
    for (const [id, fn] of Object.entries(DRAFT_FIXES)) {
      expect(typeof fn, `${id} should be callable`).toBe('function')
    }
  })
})

describe('restricting channels does the whole job', () => {
  it('clears the shared channel from the output routes too', () => {
    // Leaving it in the graph made the fix a half-measure: the review kept
    // warning about a channel the agent's own list said was gone.
    const { draft } = applyDraftFix({
      channels: ['telegram', 'http'],
      flow: { output: { channels: ['telegram', 'http'] }, nodes: [{ id: 'a', channels: ['telegram'] }] },
    }, 'restrict_to_internal_channels')
    expect(draft.flow.output.channels).toEqual(['http'])
    expect(draft.flow.nodes[0].channels).toEqual([])
  })

  it('warns when it has just removed the only way out', () => {
    const { message } = applyDraftFix({ channels: ['telegram'] }, 'restrict_to_internal_channels')
    expect(message).toMatch(/no outbound destination/i)
  })

  it('stays quiet when another destination remains', () => {
    // 'signal' is not in the shared set, so it survives and still delivers.
    const { message } = applyDraftFix({ channels: ['telegram', 'signal'] }, 'restrict_to_internal_channels')
    expect(message).not.toMatch(/no outbound destination/i)
  })

  it('leaves a graph it was not given alone', () => {
    const { draft } = applyDraftFix({ channels: ['telegram', 'http'] }, 'restrict_to_internal_channels')
    expect(draft.channels).toEqual(['http'])
  })
})

describe('writing a helper prompt', () => {
  const params = { agent: 'summarizer', prompt: 'You are Summarizer. Turn structured travel results into a short recommendation. If the input is empty, say so plainly.' }

  it('replaces the thin prompt on the named peer only', () => {
    const { draft, message } = applyDraftFix({
      new_agents: [{ id: 'notifier', system_prompt: 'keep' }, { id: 'summarizer', name: 'Summarizer', system_prompt: 'thin' }],
    }, 'write_helper_prompt', params)
    expect(draft.new_agents[0].system_prompt).toBe('keep')
    expect(draft.new_agents[1].system_prompt).toBe(params.prompt)
    expect(draft.new_agents[1].name).toBe('Summarizer')
    expect(message).toMatch(/floor, not a ceiling/)
  })

  it('creates the profile when the peer is referenced but not declared', () => {
    const { draft } = applyDraftFix({ new_agents: [] }, 'write_helper_prompt', params)
    expect(draft.new_agents).toHaveLength(1)
    expect(draft.new_agents[0].id).toBe('summarizer')
  })

  it('says so rather than silently doing nothing when no prompt was offered', () => {
    const res = applyDraftFix({ new_agents: [] }, 'write_helper_prompt', { agent: 'summarizer' })
    expect(res.draft).toBeUndefined()
    expect(res.message).toMatch(/no starter prompt/i)
  })
})

// The join-barrier fix: Studio worked out where the branches rejoin, so the
// button sets it rather than sending the user to hunt on the canvas.
describe('set_join_node', () => {
  const flow = () => ({
    entry: 'g',
    nodes: [
      { id: 'g', kind: 'llm', output: 'data' },
      { id: 'fan', kind: 'parallel', join: 'all' },
      { id: 'a', kind: 'agent', agent: 'a' },
      { id: 'b', kind: 'agent', agent: 'b' },
      { id: 'ed', kind: 'agent', agent: 'ed' },
    ],
  })

  it('sets the barrier on the named fan-out only', () => {
    const { draft, message } = applyDraftFix({ flow: flow() }, 'set_join_node', { node: 'fan', join: 'ed' })
    const byId = Object.fromEntries(draft.flow.nodes.map((n) => [n.id, n]))
    expect(byId.fan.join_node).toBe('ed')
    expect(byId.a.join_node).toBeUndefined()
    expect(message).toContain('ed')
  })

  it('does not mutate the draft it was given', () => {
    const before = { flow: flow() }
    applyDraftFix(before, 'set_join_node', { node: 'fan', join: 'ed' })
    expect(before.flow.nodes.find((n) => n.id === 'fan').join_node).toBeUndefined()
  })

  it('says so when the barrier is already set', () => {
    const d = { flow: flow() }
    d.flow.nodes[1].join_node = 'ed'
    const res = applyDraftFix(d, 'set_join_node', { node: 'fan', join: 'ed' })
    expect(res.draft).toBeUndefined()
    expect(res.message).toMatch(/already/i)
  })

  // Without both ids there is nothing to apply, and guessing would wire the
  // wrong barrier — worse than the missing one.
  it('refuses to guess when the server sent no target', () => {
    const res = applyDraftFix({ flow: flow() }, 'set_join_node', {})
    expect(res.draft).toBeUndefined()
    expect(res.message).toMatch(/could not work out/i)
  })
})
