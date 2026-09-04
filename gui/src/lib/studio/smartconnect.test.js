import { describe, it, expect } from 'vitest'
import { autoConnectEdge, explainConnection, wouldCreateCycle } from './autoconnect.js'
import { inferPython, suggestPythonSteps } from './pyinfer.js'
import { explainPythonError } from './pyerror.js'

describe('autoConnectEdge', () => {
  it('connects the newest prior work node to the new node', () => {
    const nodes = [{ id: 'a', kind: 'tool' }, { id: 'b', kind: 'python' }]
    expect(autoConnectEdge(nodes, { id: 'c', kind: 'tool' })).toEqual({ from: 'b', to: 'c' })
  })
  it('skips structural nodes and returns null for the first step', () => {
    expect(autoConnectEdge([{ id: 't', kind: 'trigger' }], { id: 'a', kind: 'tool' })).toBeNull()
    expect(autoConnectEdge([], { id: 'a', kind: 'tool' })).toBeNull()
  })
  it('does not duplicate an existing edge', () => {
    const nodes = [{ id: 'a', kind: 'tool' }]
    expect(autoConnectEdge(nodes, { id: 'b', kind: 'tool' }, [{ from: 'a', to: 'b' }])).toBeNull()
  })
})

describe('explainConnection', () => {
  it('accepts a normal connection', () => {
    expect(explainConnection({ id: 'a', kind: 'tool' }, { id: 'b', kind: 'tool' })).toBeNull()
  })
  it('rejects self-loops, post-exit, and pre-trigger', () => {
    expect(explainConnection({ id: 'a' }, { id: 'a' })).toMatch(/itself/)
    expect(explainConnection({ id: 'a', kind: 'exit' }, { id: 'b', kind: 'tool' })).toMatch(/delivery/)
    expect(explainConnection({ id: 'a', kind: 'tool' }, { id: 'b', kind: 'trigger' })).toMatch(/trigger/)
  })
})

describe('wouldCreateCycle', () => {
  it('detects a back-edge', () => {
    const edges = [{ from: 'a', to: 'b' }, { from: 'b', to: 'c' }]
    expect(wouldCreateCycle(edges, 'c', 'a')).toBe(true)
    expect(wouldCreateCycle(edges, 'a', 'c')).toBe(false)
  })
})

describe('inferPython (JS mirror)', () => {
  it('matches computation intents and skips plain ones', () => {
    expect(inferPython('Rank the top stocks').needsPython).toBe(true)
    expect(inferPython('Clean the spreadsheet').template).toBe('clean_csv')
    expect(inferPython('Summarize the news and send to Telegram').needsPython).toBe(false)
  })
})

describe('suggestPythonSteps', () => {
  it('suggests python for computational work nodes only', () => {
    const wf = { flow: { nodes: [
      { id: 'a', kind: 'tool', description: 'Rank stocks by momentum' },
      { id: 'b', kind: 'tool', description: 'Send Telegram message' },
      { id: 'c', kind: 'python', description: 'Clean data' },
    ] } }
    const s = suggestPythonSteps(wf)
    expect(s.map((x) => x.nodeId)).toEqual(['a'])
    expect(s[0].reason).toMatch(/ranking/)
  })
})

describe('explainPythonError', () => {
  it('explains common errors with a fix', () => {
    expect(explainPythonError('KeyError: "price"').summary).toMatch(/price/)
    expect(explainPythonError('KeyError: "price"').fix).toMatch(/inputs.get/)
    expect(explainPythonError("ModuleNotFoundError: No module named 'pandas'").summary).toMatch(/pandas/)
    expect(explainPythonError('ZeroDivisionError: division by zero').summary).toMatch(/zero/)
  })
  it('falls back to the last traceback line', () => {
    expect(explainPythonError('Traceback...\nWeirdError: something odd').summary).toMatch(/something odd/)
    expect(explainPythonError('').summary).toMatch(/without an error/)
  })
})

// ── Platform (engine) errors ────────────────────────────────────────────────
// These used to fall through to "Check the code around the reported line",
// which is actively wrong for a consent refusal: the code is fine, it just
// needs approval.
describe('explainPythonError — platform errors', () => {
  const CONSENT = 'engine: workflow: flow: node "curate_source_pack": consent: node "curate_source_pack" runs beyond guardrails ([network dynamic]) but has no consent grant'

  it('explains a consent refusal in plain language and offers a grant action', () => {
    const r = explainPythonError(CONSENT)
    expect(r.summary).toMatch(/curate_source_pack/)
    expect(r.summary).toMatch(/network calls/)
    expect(r.summary).toMatch(/dynamic code/)
    // It must NOT imply the code is broken.
    expect(r.summary).toMatch(/Nothing is wrong with the code/)
    expect(r.action).toEqual({ kind: 'consent', nodeId: 'curate_source_pack' })
  })

  it('never returns the useless code-line advice for a consent error', () => {
    expect(explainPythonError(CONSENT).fix).not.toMatch(/around the reported line/)
  })

  it('routes a stale consent stamp to the grant dialog too', () => {
    const r = explainPythonError('consent: node "parse" code changed since it was consented — re-consent required')
    expect(r.action).toEqual({ kind: 'consent', nodeId: 'parse' })
  })

  it('explains a web_search timeout as a provider problem and points at the timeout', () => {
    const r = explainPythonError('flow: node "search_article_sources": item 4: web_search: request failed: Post "https://ollama.com/api/web_search": context deadline exceeded (Client.Timeout exceeded while awaiting headers)')
    expect(r.summary).toMatch(/didn’t answer in time/)
    expect(r.fix).toMatch(/timeout_s|search\.timeout/)
    expect(r.action).toEqual({ kind: 'timeout', nodeId: 'search_article_sources' })
  })

  it('does not offer a Studio action for the server-side system ceiling', () => {
    const r = explainPythonError("flow: node \"x\": shell_exec requires the 'system' capability")
    expect(r.summary).toMatch(/host machine/)
    expect(r.action).toBeUndefined()
  })

  it('routes missing tools, bad credentials and channel failures to the right place', () => {
    expect(explainPythonError('flow: node "x": no such tool: mcp__foo__bar').action).toEqual({ kind: 'tools' })
    expect(explainPythonError('web_search: no Ollama API key. Create one at https://ollama.com/settings/keys').action).toEqual({ kind: 'providers' })
    expect(explainPythonError('channel.send: send failed through channel "telegram": chat not found').action).toEqual({ kind: 'channels' })
  })

  it('still handles Python tracebacks, with no action', () => {
    const r = explainPythonError('KeyError: "price"')
    expect(r.summary).toMatch(/price/)
    expect(r.action).toBeUndefined()
  })
})
