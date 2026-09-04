// Vitest shape-regression suite for the Cohort F security helpers.
// Every case here pins one contract the backend already emits — when the
// backend event or endpoint shape drifts, this suite breaks BEFORE the
// operator sees an undefined chip or a blank chip on the Activity page.
import { describe, it, expect } from 'vitest'
import {
  toolResultPayload,
  injectionInfo,
  intentInfo,
  hasSecuritySignal,
  severityClass,
  decisionClass,
  privilegedContext,
  securityHighlight,
  applySecurityRecommendation,
} from './security.js'

describe('toolResultPayload', () => {
  it('unwraps the {tool_result, injection} envelope from S2 findings', () => {
    const p = { tool_result: { name: 'fetch_url', content: 'x' }, injection: { max_severity: 'high' } }
    expect(toolResultPayload(p)).toEqual({ name: 'fetch_url', content: 'x' })
  })

  it('passes through a bare ToolResult unchanged', () => {
    const p = { name: 'kb_search', content: 'hit' }
    expect(toolResultPayload(p)).toBe(p)
  })

  it('returns empty object for null / undefined so callers can safely destructure', () => {
    expect(toolResultPayload(null)).toEqual({})
    expect(toolResultPayload(undefined)).toEqual({})
  })
})

describe('injectionInfo', () => {
  it('finds injection sub-object on tool.result payloads', () => {
    const ev = {
      type: 'tool.result',
      payload: { tool_result: { name: 'fetch_url' }, injection: { max_severity: 'high', findings: [] } },
    }
    expect(injectionInfo(ev)).toEqual({ max_severity: 'high', findings: [] })
  })

  it('returns the payload directly for injection.finding events', () => {
    const ev = { type: 'injection.finding', payload: { source: 'fetch_url', max_severity: 'medium' } }
    expect(injectionInfo(ev)).toEqual({ source: 'fetch_url', max_severity: 'medium' })
  })

  it('returns null when tool.result carries no injection block', () => {
    const ev = { type: 'tool.result', payload: { name: 'kb_search', content: 'ok' } }
    expect(injectionInfo(ev)).toBeNull()
  })

  it('returns null for unrelated event types', () => {
    expect(injectionInfo({ type: 'llm.result', payload: {} })).toBeNull()
    expect(injectionInfo(null)).toBeNull()
  })
})

describe('intentInfo', () => {
  it('extracts intent.decision payload', () => {
    const ev = { type: 'intent.decision', payload: { decision: 'deny', tool: 'shell_exec', reason: '…' } }
    expect(intentInfo(ev)).toEqual({ decision: 'deny', tool: 'shell_exec', reason: '…' })
  })

  it('returns null for non-intent events', () => {
    expect(intentInfo({ type: 'tool.result', payload: {} })).toBeNull()
    expect(intentInfo(null)).toBeNull()
  })
})

describe('hasSecuritySignal', () => {
  it('reports true when tool.result carries a max_severity > none', () => {
    const ev = { type: 'tool.result', payload: { tool_result: {}, injection: { max_severity: 'high' } } }
    expect(hasSecuritySignal(ev)).toBe(true)
  })

  it('reports true when the runtime emitted any intent.decision event', () => {
    const ev = { type: 'intent.decision', payload: { decision: 'allow' } }
    expect(hasSecuritySignal(ev)).toBe(true)
  })

  it('reports false when injection max_severity is none', () => {
    const ev = { type: 'tool.result', payload: { tool_result: {}, injection: { max_severity: 'none' } } }
    expect(hasSecuritySignal(ev)).toBe(false)
  })

  it('reports false for regular tool.result events with no injection block', () => {
    const ev = { type: 'tool.result', payload: { name: 'kb_search', content: 'ok' } }
    expect(hasSecuritySignal(ev)).toBe(false)
  })
})

describe('severityClass', () => {
  it('maps severities to the deployment-profile CSS palette', () => {
    expect(severityClass('high')).toBe('danger')
    expect(severityClass('medium')).toBe('warn')
    expect(severityClass('low')).toBe('warn')
    expect(severityClass('info')).toBe('info')
    expect(severityClass('none')).toBe('')
  })

  it('normalizes case + defaults unknown values to info', () => {
    expect(severityClass('HIGH')).toBe('danger')
    expect(severityClass('mystery')).toBe('info')
    expect(severityClass(undefined)).toBe('info')
  })
})

describe('decisionClass', () => {
  it('colors deny as danger, prompt as warn, allow as info', () => {
    expect(decisionClass('deny')).toBe('danger')
    expect(decisionClass('prompt')).toBe('warn')
    expect(decisionClass('allow')).toBe('info')
  })
})

describe('privilegedContext', () => {
  const tiers = {
    'priv-agent': { tier: 'privileged', reasons: ['file write'] },
    'active-agent': { tier: 'active', reasons: [] },
    'ro-agent':    { tier: 'read_only', reasons: [] },
  }

  it('returns null for non-privileged agents (no callout renders)', () => {
    expect(privilegedContext('active-agent', {}, tiers, 'production')).toBeNull()
    expect(privilegedContext('ro-agent', {}, tiers, 'production')).toBeNull()
    expect(privilegedContext('unknown', {}, tiers, 'production')).toBeNull()
    expect(privilegedContext('', {}, tiers, 'production')).toBeNull()
  })

  it('reports needsAck=true when accept_privileged_exposure is missing', () => {
    const ctx = privilegedContext('priv-agent', {}, tiers, 'staging')
    expect(ctx).toMatchObject({ tier: 'privileged', accepted: false, needsAck: true, productionBlock: false })
  })

  it('escalates to productionBlock=true only under the production profile', () => {
    const staging = privilegedContext('priv-agent', {}, tiers, 'staging')
    expect(staging.productionBlock).toBe(false)
    const prod = privilegedContext('priv-agent', {}, tiers, 'production')
    expect(prod.productionBlock).toBe(true)
  })

  it('honours accept_privileged_exposure=true (string OR bool) so productionBlock stops firing', () => {
    const asBool = privilegedContext('priv-agent', { accept_privileged_exposure: true }, tiers, 'production')
    expect(asBool.accepted).toBe(true)
    expect(asBool.needsAck).toBe(false)
    expect(asBool.productionBlock).toBe(false)
    const asString = privilegedContext('priv-agent', { accept_privileged_exposure: 'true' }, tiers, 'production')
    expect(asString.accepted).toBe(true)
    expect(asString.productionBlock).toBe(false)
  })
})

describe('securityHighlight', () => {
  it('returns null when the readiness payload is absent', () => {
    expect(securityHighlight(null)).toBeNull()
    expect(securityHighlight({})).toBeNull()
  })

  it('surfaces unaccepted privileged exposure ahead of everything else', () => {
    const readiness = {
      privileged_exposures: [
        { agent_id: 'a1', agent_name: 'Reader', channels: ['telegram'], accepted: true },
        { agent_id: 'a2', agent_name: 'Writer', channels: ['slack'], accepted: false },
      ],
      wildcard_mcp_agents: ['a3'],
      next_actions: ['do the thing'],
    }
    expect(securityHighlight(readiness)).toEqual({
      text: 'Writer exposes slack without ack',
      agentId: 'a2',
    })
  })

  it('falls back to any privileged exposure when all are already accepted', () => {
    const readiness = {
      privileged_exposures: [
        { agent_id: 'a1', agent_name: 'Reader', channels: ['telegram'], accepted: true },
      ],
      wildcard_mcp_agents: ['a3'],
    }
    const hl = securityHighlight(readiness)
    expect(hl.agentId).toBe('a1')
    expect(hl.text).toContain('privileged channels')
  })

  it('falls back to wildcard MCP when no exposures exist', () => {
    const readiness = { wildcard_mcp_agents: ['a3'], next_actions: ['other'] }
    expect(securityHighlight(readiness)).toEqual({
      text: 'wildcard MCP allow-list on a3',
      agentId: 'a3',
    })
  })

  it('falls back to the top next_action when nothing else applies', () => {
    const readiness = { next_actions: ['configure security.intent_gate: deny'] }
    expect(securityHighlight(readiness)).toEqual({
      text: 'configure security.intent_gate: deny',
      agentId: '',
    })
  })
})

describe('applySecurityRecommendation', () => {
  it('rewrites tool nodes for a bare-identifier suggestion (write_file → kb_write)', () => {
    const workflow = {
      name: 'demo',
      flow: {
        nodes: [
          { id: 'n1', tool: 'write_file' },
          { id: 'n2', tool: 'kb_search' },
        ],
      },
    }
    const { workflow: next, hits } = applySecurityRecommendation(
      { from: 'write_file', suggest: 'kb_write', reason: 'safer' },
      workflow,
    )
    expect(hits).toBe(1)
    expect(next.flow.nodes[0].tool).toBe('kb_write')
    expect(next.flow.nodes[1].tool).toBe('kb_search')
    // Original workflow must not be mutated in place — Studio relies on the
    // returned copy for its setWorkflow call.
    expect(workflow.flow.nodes[0].tool).toBe('write_file')
  })

  it('also rewrites nested config.tool references', () => {
    const workflow = {
      flow: { nodes: [{ id: 'n1', config: { tool: 'write_file', other: 42 } }] },
    }
    const { workflow: next, hits } = applySecurityRecommendation(
      { from: 'write_file', suggest: 'kb_write' },
      workflow,
    )
    expect(hits).toBe(1)
    expect(next.flow.nodes[0].config.tool).toBe('kb_write')
    expect(next.flow.nodes[0].config.other).toBe(42)
  })

  it('reports hits=0 and workflow=null when no node uses the from-tool', () => {
    const workflow = { flow: { nodes: [{ id: 'n1', tool: 'kb_search' }] } }
    const res = applySecurityRecommendation(
      { from: 'write_file', suggest: 'kb_write' },
      workflow,
    )
    expect(res).toEqual({ workflow: null, hits: 0 })
  })

  it('refuses natural-language suggestions (auto-apply only makes sense for bare identifiers)', () => {
    const workflow = { flow: { nodes: [{ id: 'n1', tool: 'shell_exec' }] } }
    const res = applySecurityRecommendation(
      { from: 'shell_exec', suggest: 'a scoped Python tool via python_file' },
      workflow,
    )
    expect(res).toEqual({ workflow: null, hits: 0 })
  })

  it('is a no-op when required fields are missing', () => {
    expect(applySecurityRecommendation(null, { flow: { nodes: [] } })).toEqual({ workflow: null, hits: 0 })
    expect(applySecurityRecommendation({ from: 'x', suggest: 'y' }, null)).toEqual({ workflow: null, hits: 0 })
    expect(applySecurityRecommendation({ from: '', suggest: 'y' }, { flow: {} })).toEqual({ workflow: null, hits: 0 })
  })
})
