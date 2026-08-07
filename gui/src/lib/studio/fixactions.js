// fixactions.js — the client half of the remediation vocabulary.
//
// The Go half is internal/studio/fixactions.go. Every id it can emit must be
// handled here; every id handled here must exist there. fixactions.test.js
// reads both files and fails the build in either direction, because this seam
// has drifted twice already: Studio.svelte once handled five actions the server
// never sent while six it did send fell through to a no-op, and the security
// panel later grew a second vocabulary of its own.
//
// Everything here is pure. Applying a fix returns a NEW draft and a sentence
// describing what changed; it never touches the DOM, navigates, or reads a
// store. That keeps the interesting half — what the button actually does to
// your workflow — testable without mounting a 10k-line component.

/** Actions that leave Studio for the screen that owns the setting. */
export const NAVIGATE_TARGETS = {
  open_providers: '#providers',
  open_mcp: '#mcp',
  open_delivery: '#channels',
  open_secrets: '#secrets',
}

/** Actions the host handles in place: focus a node, open the bench, etc. */
export const FOCUS_ACTIONS = [
  'choose_model',
  'add_assertions',
  'run_live',
  'open_studio',
  'open_preflight',
  'reveal_node',
  'rename_agent',
]

// Channels that reach people outside the install — the exact set in
// studioSharedExternalChannels (internal/studio/security_preflight.go). The
// first version of this list was hand-typed and got two of them wrong
// ("googlechat" for google_chat, and sms missing entirely), so the button would
// have claimed an agent was internal-only while leaving those two wired up.
// fixactions.test.js now reads the Go map itself rather than a pattern someone
// typed twice.
export const SHARED_CHANNELS = [
  'telegram', 'slack', 'discord',
  'whatsapp', 'whatsapp_web',
  'email', 'teams', 'google_chat',
  'sms', 'webhook',
]

const isShared = (c) => SHARED_CHANNELS.includes(String(c || '').toLowerCase())

/**
 * Fixes Studio applies to the draft itself.
 * Each returns { draft, message } on success, or { message } with no draft when
 * there was nothing to do — the caller reports that rather than silently
 * "succeeding" on a no-op.
 */
export const DRAFT_FIXES = {
  restrict_to_internal_channels(draft) {
    const before = Array.isArray(draft.channels) ? draft.channels : []
    const removed = before.filter(isShared)
    if (!removed.length) {
      return { message: 'This agent is already on internal channels only.' }
    }
    const kept = before.filter((c) => !isShared(c))
    // Leave it reachable. An agent bound to nothing at all is a different kind
    // of broken from an agent bound to too much.
    if (!kept.includes('http')) kept.push('http')

    // The graph's output node routes to channels too. Leaving those behind made
    // the fix a half-measure: the security review still saw the shared channel
    // in the graph and kept warning, while the agent's channel list said it had
    // been dealt with.
    const flow = draft.flow || {}
    const output = flow.output && typeof flow.output === 'object'
      ? { ...flow.output, channels: (flow.output.channels || []).filter((c) => !isShared(c)) }
      : flow.output
    const nodes = Array.isArray(flow.nodes)
      ? flow.nodes.map((n) => (n && Array.isArray(n.channels) && n.channels.some(isShared)
        ? { ...n, channels: n.channels.filter((c) => !isShared(c)) }
        : n))
      : flow.nodes

    // Say the consequence out loud. Removing the only real destination from an
    // agent whose job is delivery turns one warning into two blockers, and the
    // user should hear that from the button rather than from the Save step.
    const stillDelivers = kept.some((c) => !['http', 'webhook', ''].includes(String(c).toLowerCase()))
    const consequence = stillDelivers
      ? ''
      : ' It now has no outbound destination — pick another one, or accept the exposure per binding on the Delivery page instead.'

    return {
      draft: { ...draft, channels: kept, flow: { ...flow, output, nodes } },
      message: `Removed ${removed.join(', ')} — this agent is now on internal HTTP only.${consequence}`,
    }
  },

  // The synthesized persona travels in the finding, so the click is instant and
  // the client never has to know what a good agent prompt looks like.
  write_helper_prompt(draft, params = {}) {
    const id = String(params.agent || '').trim()
    const prompt = String(params.prompt || '').trim()
    if (!id || !prompt) {
      return { message: 'No starter prompt was offered for this helper agent.' }
    }
    const peers = Array.isArray(draft.new_agents) ? draft.new_agents : []
    const i = peers.findIndex((p) => String(p && p.id || '').toLowerCase() === id.toLowerCase())
    if (i < 0) {
      // The peer is referenced by a node but has no profile entry yet, so make
      // one rather than dropping the fix on the floor.
      return {
        draft: { ...draft, new_agents: [...peers, { id, name: id, system_prompt: prompt }] },
        message: `Wrote a starter prompt for ${id}. Read it over — it is a floor, not a ceiling.`,
      }
    }
    const next = peers.slice()
    next[i] = { ...next[i], system_prompt: prompt }
    return {
      draft: { ...draft, new_agents: next },
      message: `Wrote a starter prompt for ${id}. Read it over — it is a floor, not a ceiling.`,
    }
  },

  set_intent_gate_deny(draft) {
    if ((draft.security || {}).intent_gate === 'deny') {
      return { message: 'The intent gate is already set to deny.' }
    }
    return {
      draft: { ...draft, security: { ...(draft.security || {}), intent_gate: 'deny' } },
      message: 'Intent gate set to deny — injection-steered tool calls will be refused.',
    }
  },
}

/** Every id this module knows how to handle. */
export function handledActionIds() {
  return [
    ...Object.keys(NAVIGATE_TARGETS),
    ...FOCUS_ACTIONS,
    ...Object.keys(DRAFT_FIXES),
  ].sort()
}

/** What kind of thing an id is, or '' when it is not in the vocabulary. */
export function actionKind(id) {
  if (!id) return ''
  if (NAVIGATE_TARGETS[id]) return 'navigate'
  if (DRAFT_FIXES[id]) return 'apply'
  if (FOCUS_ACTIONS.includes(id)) return 'focus'
  return ''
}

/**
 * Apply a draft-editing fix. Returns { draft, message } — `draft` is undefined
 * when nothing changed. Throws for an unknown id, because a caller silently
 * dropping one is exactly the failure this module exists to prevent.
 */
export function applyDraftFix(draft, action, params) {
  const fn = DRAFT_FIXES[action]
  if (!fn) throw new Error(`no draft fix for action "${action}"`)
  if (!draft) return { message: 'There is no draft to change yet.' }
  return fn(draft, params || {})
}

/** The button text, when the server did not send one. */
export function fallbackLabel(action) {
  return actionKind(action) ? 'Fix this' : ''
}
