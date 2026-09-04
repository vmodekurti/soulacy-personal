// Framework-level security helpers (Cohort G / audit gap fix). Pure
// functions extracted from Activity.svelte, Channels.svelte, Dashboard.svelte,
// and Studio.svelte so their shape contracts can be pinned by vitest instead
// of only being exercised at render time.
//
// Every function here is deterministic, has no imports, and takes plain JSON
// shapes — the same shapes the backend endpoints in internal/injection,
// internal/intent, internal/securitydoctor, and internal/gateway/
// securityreadiness.go emit. When the backend event shape drifts, these tests
// break first, before an operator sees an undefined chip on the Activity page.

// ─── Cohort F tool.result envelope unwrapping ───────────────────────────────
// When the runtime detects prompt-injection findings on a tool result, it
// wraps the payload as {tool_result:{…}, injection:{…}} (engine.go:3560).
// toolResultPayload returns the raw ToolResult regardless of which shape the
// payload takes so summary/chip/action code sees a consistent shape.
export function toolResultPayload(p) {
  if (!p) return {}
  if (p.tool_result && typeof p.tool_result === 'object') return p.tool_result
  return p
}

// Extract the injection.finding sub-object from any event that might carry
// one. Returns null when there is no injection signal (or the caller passed
// a wrong event type). Consumed by Activity.svelte to decide whether to
// render the injection chip.
export function injectionInfo(ev) {
  if (!ev) return null
  if (ev.type === 'tool.result') {
    const p = ev.payload || {}
    if (p && typeof p === 'object' && p.injection && p.tool_result) return p.injection
    return null
  }
  if (ev.type === 'injection.finding') return ev.payload || null
  return null
}

// Extract the intent.decision payload from an event. Returns null for any
// other event type so callers can pattern-match cheaply.
export function intentInfo(ev) {
  if (!ev || ev.type !== 'intent.decision') return null
  return ev.payload || null
}

// True when the event carries any surfaceable security signal (either an
// injection finding above 'none' or an intent decision that ran). The intent
// runtime only emits decision events on Deny, Prompt, or Allow-with-influence
// (engine.go:3677), so an emitted event is itself a signal.
export function hasSecuritySignal(ev) {
  const inj = injectionInfo(ev)
  if (inj && inj.max_severity && inj.max_severity !== 'none') return true
  const dec = intentInfo(ev)
  if (dec) return true
  return false
}

// UI severity/decision colour classifiers. Strings match the deployment
// severity palette used across Dashboard / Doctor / Studio.
export function severityClass(sev) {
  switch ((sev || '').toLowerCase()) {
    case 'high':   return 'danger'
    case 'medium': return 'warn'
    case 'low':    return 'warn'
    case 'info':   return 'info'
    case 'none':   return ''
    default:       return 'info'
  }
}
export function decisionClass(dec) {
  switch ((dec || '').toLowerCase()) {
    case 'deny':   return 'danger'
    case 'prompt': return 'warn'
    case 'allow':  return 'info'
    default:       return 'info'
  }
}

// ─── Cohort F Channels: privileged-exposure context ─────────────────────────
// Returns the callout state for a channel binding, or null when the bound
// agent isn't privileged (caller renders nothing in that case). Extracted
// from Channels.svelte:434-464 so its precedence rules are testable in
// isolation.
//
// Args:
//   agentId — the agent bound to the channel (either form.agent_id or bot.agent_id)
//   values  — the binding's raw settings, notably accept_privileged_exposure
//   tiers   — { [id]: { tier, reasons } } as returned by api.agents.tier
//   deploymentProfile — the workspace deployment profile string; drives the
//                        production-fail escalation.
export function privilegedContext(agentId, values, tiers, deploymentProfile) {
  if (!agentId) return null
  const info = tiers && tiers[agentId]
  if (!info || info.tier !== 'privileged') return null
  const accepted = isTruthy(values && values.accept_privileged_exposure)
  return {
    agentId,
    tier: info.tier,
    reasons: info.reasons || [],
    accepted,
    needsAck: !accepted,
    // In production, unaccepted privileged exposure escalates readiness from
    // warn to fail (internal/gateway/securityreadiness.go verdict matrix).
    productionBlock: !accepted && deploymentProfile === 'production',
  }
}

function isTruthy(value) {
  if (value === undefined || value === null) return false
  return value === true || String(value).toLowerCase() === 'true'
}

// ─── Cohort F Dashboard: security highlight picker ──────────────────────────
// Given the /security/readiness response, picks the single highest-priority
// action to surface inline on the Security row of the journey grid.
// Extracted from Dashboard.svelte:170-198 so the ordering rule (unaccepted
// privileged > privileged > wildcard MCP > next-action) can be pinned by a
// test — otherwise it drifts as new sub-checks land.
export function securityHighlight(readiness) {
  if (!readiness) return null
  const exposures = readiness.privileged_exposures || []
  const unaccepted = exposures.filter(e => !e.accepted)
  if (unaccepted.length > 0) {
    const first = unaccepted[0]
    return {
      text: `${first.agent_name || first.agent_id} exposes ${(first.channels || []).join(', ') || 'shared channels'} without ack`,
      agentId: first.agent_id,
    }
  }
  if (exposures.length > 0) {
    const first = exposures[0]
    return {
      text: `${first.agent_name || first.agent_id} privileged channels: ${(first.channels || []).join(', ') || 'shared channels'}`,
      agentId: first.agent_id,
    }
  }
  const wildcards = readiness.wildcard_mcp_agents || []
  if (wildcards.length > 0) {
    return { text: `wildcard MCP allow-list on ${wildcards[0]}`, agentId: wildcards[0] }
  }
  if (readiness.next_actions && readiness.next_actions.length > 0) {
    return { text: readiness.next_actions[0], agentId: '' }
  }
  return null
}

// ─── Cohort F Studio: security recommendation applier ───────────────────────
// Extracted from Studio.svelte's applySecurityRecommendation so the tool
// rewrite semantics can be pinned. Returns { workflow, hits } — workflow is a
// shallow-copied replacement (null if no rewrite occurred and caller should
// leave the draft untouched), hits is the count of nodes rewritten.
//
// Only unambiguous single-identifier suggestions are auto-applied. The
// backend at internal/studio/security_preflight.go:224-247 emits some
// natural-language "consider using an MCP server" suggestions — those match
// the isBareToolIdentifier guard and get skipped so the operator handles them
// manually.
export function applySecurityRecommendation(rec, workflow) {
  if (!rec || !rec.from || !rec.suggest || !workflow) return { workflow: null, hits: 0 }
  const from = String(rec.from)
  if (!isBareToolIdentifier(rec.suggest)) return { workflow: null, hits: 0 }
  const suggestId = rec.suggest
  const flow = workflow.flow || {}
  const nodes = Array.isArray(flow.nodes) ? flow.nodes : []
  let hits = 0
  const nextNodes = nodes.map((n) => {
    if (!n) return n
    const cfg = n.config || {}
    if (n.tool === from || cfg.tool === from) {
      hits++
      return {
        ...n,
        tool: n.tool === from ? suggestId : n.tool,
        config: cfg.tool === from ? { ...cfg, tool: suggestId } : cfg,
      }
    }
    return n
  })
  if (!hits) return { workflow: null, hits: 0 }
  return {
    workflow: { ...workflow, flow: { ...flow, nodes: nextNodes } },
    hits,
  }
}

function isBareToolIdentifier(s) {
  return typeof s === 'string' && /^[a-zA-Z0-9_.-]+$/.test(s)
}
