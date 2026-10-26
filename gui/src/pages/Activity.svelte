<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { get } from 'svelte/store'
  import { api } from '../lib/api.js'
  import { activityAgent, studioDebugRun } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import RunFailure from '../lib/RunFailure.svelte'

  let agents     = []
  let selectedId = ''
  let events     = []
  let path       = ''
  let loading    = false
  let error      = ''
  let watching   = false
  let timer      = null
  let typeFilter = 'all'
  let logEl
  let autoScroll = true
  let replayingSession = ''
  let replayMsg = ''
  let learningSession = ''
  let routeAgentId = ''
  let sessionFilter = ''
  let runHistory = []
  let runFilter = null
  // F-GUI-1 — Cohort F security signals: expanded rows for injection findings
  // and intent decisions so operators can drill into the reason without
  // hunting through the raw payload.
  let expandedRows = new Set()
  // securityOnly is a cross-cutting filter on top of typeFilter: when true,
  // rows without an injection/intent security signal are hidden.
  let securityOnly = false
  // E4c — hung-session tracker: polled from /activity/running so the "Running now"
  // strip can surface stalled sessions with an actionable reason + fix.
  let runningSessions = []
  let hungCount = 0
  let runningPollTimer = null
  let activityFocused = false
  let showAdvancedFilters = false
  const ALL_AGENTS = '__all__'
  const PRIMARY_FILTERS = ['all', 'run', 'errors']
  const ADVANCED_FILTERS = ['llm', 'tools', 'decisions', 'security', 'flow']

  function hashParams() {
    const hash = window.location.hash || ''
    const idx = hash.indexOf('?')
    return new URLSearchParams(idx >= 0 ? hash.slice(idx + 1) : '')
  }

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      const preset = get(activityAgent)
      if (routeAgentId && agents.find(a => a.id === routeAgentId)) selectedId = routeAgentId
      else if (preset && agents.find(a => a.id === preset)) selectedId = preset
      else if (!selectedId) selectedId = ALL_AGENTS
      if (selectedId) await poll()
      // Auto-start watching when deep-linked from a "Watch" button.
      if (preset || routeAgentId || sessionFilter) { activityAgent.set(''); if (!watching) toggleWatch() }
    } catch (e) { error = e.message }
  }

  async function poll() {
    if (!selectedId) return
    loading = events.length === 0
    try {
      const [res, hist] = await Promise.all([
        selectedId === ALL_AGENTS
          ? api.runs.events({ limit: 1000, sessionId: sessionFilter })
          : api.agents.actions(selectedId, 500),
        api.runs.ledger({
          agentId: selectedId === ALL_AGENTS ? '' : selectedId,
          limit: 250,
          eventLimit: 50000,
        }).catch(() => ({ runs: [] })),
      ])
      events = res.events || []
      path   = selectedId === ALL_AGENTS ? '' : (res.path || '')
      runHistory = normalizeRunHistory(hist.runs || [])
      if (runFilter && !runHistory.find(r => r.id === runFilter.id)) runFilter = null
      error  = ''
      if (autoScroll) scrollToBottom()
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function scrollToBottom() {
    setTimeout(() => { if (logEl) logEl.scrollTop = logEl.scrollHeight }, 40)
  }

  function toggleWatch() {
    watching = !watching
    if (watching) {
      poll()
      timer = setInterval(poll, 2000)
    } else {
      clearInterval(timer); timer = null
    }
  }

  async function selectAgent(id) {
    selectedId = id
    sessionFilter = ''
    runFilter = null
    runHistory = []
    events = []
    path = ''
    replayMsg = ''
    await poll()
  }

  async function replayRun(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id || replayingSession) return
    replayingSession = ev.session_id
    replayMsg = ''
    try {
      const res = await api.agents.replay(agentId, ev.session_id)
      replayMsg = `Replayed ${ev.session_id} as ${res.replay_session_id || 'new session'}`
      await poll()
    } catch (e) {
      error = e.message || 'Replay failed'
    } finally {
      replayingSession = ''
    }
  }

  async function learnFromRun(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id || learningSession) return
    learningSession = ev.session_id
    replayMsg = ''
    try {
      const res = await api.brainMemory.proposeFromRun(agentId, ev.session_id, 3)
      const n = res.created || (res.proposals || []).length || 0
      replayMsg = n
        ? `Created ${n} learning proposal${n === 1 ? '' : 's'} from ${ev.session_id}. Review them in Learning.`
        : `No learning proposals created from ${ev.session_id}. The run may be too short to generalize.`
    } catch (e) {
      error = e.message || 'Learning proposal failed'
    } finally {
      learningSession = ''
    }
  }

  function debugInStudio(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id) return
    studioDebugRun.set({
      agentId,
      sessionId: ev.session_id,
      error: eventErrorText(ev),
    })
    window.location.hash = '#studio'
  }

  function isBrowserEvent(ev) {
    if (!ev?.session_id || !ev.type?.startsWith('tool.')) return false
    const p = ev.payload || {}
    const name = String(p.name || '').toLowerCase()
    return name.includes('browser') ||
      name.includes('playwright') ||
      name.includes('puppeteer') ||
      name.includes('computer') ||
      name.includes('navigate') ||
      name.includes('screenshot') ||
      name.includes('page_')
  }

  function openBrowserTrace(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id) return
    const params = new URLSearchParams({ agent_id: agentId, session_id: ev.session_id })
    window.location.hash = `#browser?${params.toString()}`
  }

  function openRunBrowserTrace(run) {
    if (!run?.agentId || !run?.sessionId) return
    const params = new URLSearchParams({ agent_id: run.agentId, session_id: run.sessionId })
    window.location.hash = `#browser?${params.toString()}`
  }

  onMount(() => {
    const params = hashParams()
    routeAgentId = params.get('agent_id') || ''
    sessionFilter = params.get('session_id') || ''
    loadAgents()
    // E4c — poll the hung-session tracker every 3s. This is independent of
    // the per-agent event stream so the "Running now" strip stays live even
    // when the user isn't watching a specific agent.
    pollRunning()
    runningPollTimer = setInterval(pollRunning, 3000)
    window.addEventListener('keydown', handleActivityKeydown)
  })
  onDestroy(() => {
    if (timer) clearInterval(timer)
    if (runningPollTimer) clearInterval(runningPollTimer)
    window.removeEventListener('keydown', handleActivityKeydown)
  })

  function handleActivityKeydown(event) {
    if (event.key === 'Escape' && activityFocused) activityFocused = false
  }

  function toggleActivityFocus() {
    activityFocused = !activityFocused
    if (activityFocused) setTimeout(() => logEl?.focus(), 0)
  }

  async function pollRunning() {
    try {
      const r = await api.activity.running()
      runningSessions = r.sessions || []
      hungCount = r.hung_count || 0
    } catch (e) {
      // Silent — the strip is best-effort observability; failure shouldn't
      // spam the top-of-page banner.
    }
  }

  function watchHungSession(sess) {
    if (!sess?.agent_id || !sess?.session_id) return
    const q = new URLSearchParams({ agent_id: sess.agent_id, session_id: sess.session_id })
    window.location.hash = `#activity?${q.toString()}`
    selectedId = sess.agent_id
    sessionFilter = sess.session_id
    poll()
  }

  function fmtDuration(seconds) {
    const s = Number(seconds) || 0
    if (s < 60) return `${s}s`
    if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  }

  // ── rendering helpers ──────────────────────────────────────────────
  const TYPE_META = {
    'message.in':  { label: 'RUN',    color: 'var(--sl-accent)', icon: '▶' },
    'llm.call':    { label: 'LLM',    color: 'var(--sl-accent-hover)', icon: '↗' },
    'llm.result':  { label: 'LLM',    color: 'var(--sl-accent-hover)', icon: '↙' },
    'llm.prompt':  { label: 'PROMPT', color: '#8b93bd', icon: '▧' },
    'llm.completion': { label: 'LLM', color: 'var(--sl-accent-hover)', icon: '◈' },
    'llm.response.content': { label: 'OUTPUT', color: '#8b93bd', icon: '▤' },
    'llm.route.decision':  { label: 'ROUTE', color: '#8bdcff', icon: '◇' },
    'llm.route.fallback':  { label: 'ROUTE', color: '#f0a060', icon: '⇢' },
    'llm.route.completed': { label: 'ROUTE', color: '#4caf82', icon: '◆' },
    'context.prepared':  { label: 'CONTEXT', color: '#9ba7ff', icon: '▧' },
    'context.compacted': { label: 'CONTEXT', color: '#f0a060', icon: '▨' },
    'lifecycle.run.before': { label: 'HOOK', color: '#c084fc', icon: '◌' },
    'lifecycle.run.after': { label: 'HOOK', color: '#c084fc', icon: '●' },
    'lifecycle.context.after': { label: 'HOOK', color: '#c084fc', icon: '◌' },
    'lifecycle.llm.before': { label: 'HOOK', color: '#c084fc', icon: '◌' },
    'lifecycle.llm.after': { label: 'HOOK', color: '#c084fc', icon: '●' },
    'lifecycle.tool.before': { label: 'HOOK', color: '#c084fc', icon: '◌' },
    'lifecycle.tool.after': { label: 'HOOK', color: '#c084fc', icon: '●' },
    'tool.call':   { label: 'TOOL',   color: '#f0a060', icon: '🔧' },
    'tool.result': { label: 'TOOL',   color: '#f0a060', icon: '↩' },
    'reasoning.start':  { label: 'LOOP', color: '#5bc0de', icon: '▶' },
    'reasoning.step':   { label: 'LOOP', color: '#5bc0de', icon: '∴' },
    'reasoning.result': { label: 'LOOP', color: '#5bc0de', icon: '■' },
    'flow.node.started': { label: 'FLOW', color: '#40c4ff', icon: '▶' },
    'flow.node':         { label: 'FLOW', color: '#40c4ff', icon: '•' },
    'message.out': { label: 'REPLY',  color: '#4caf82', icon: '✓' },
    'error':       { label: 'ERROR',  color: '#f06060', icon: '✖' },
    'connected':   { label: 'SYS',    color: '#555a7a', icon: '•' },
    // F-GUI-1 — Cohort F security events surface alongside runtime events.
    'injection.finding': { label: 'INJECT', color: '#f0a060', icon: '⚠' },
    'intent.decision':   { label: 'INTENT', color: '#c084fc', icon: '⚑' },
  }
  function meta(t) { return TYPE_META[t] || { label: (t || 'evt').toUpperCase().slice(0, 5), color: '#6b7294', icon: '•' } }

  function snippet(s, n = 120) { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s }

  function safeParseJSON(v) {
    if (v == null) return null
    if (typeof v === 'object') return v
    const s = String(v).trim()
    if (!s) return null
    try { return JSON.parse(s) } catch (_) { return null }
  }

  function flowPayload(ev) {
    if (!ev?.type?.startsWith('flow.')) return null
    const p = ev.payload || {}
    if (typeof p === 'string') return safeParseJSON(p) || { raw: p }
    return p
  }

  function shortValue(v, n = 160) {
    if (v == null || v === '') return ''
    const parsed = safeParseJSON(v)
    if (parsed && typeof parsed === 'object') {
      const pairs = []
      for (const [k, val] of Object.entries(parsed)) {
        if (val == null || val === '') continue
        if (typeof val === 'string') pairs.push(`${k}=${JSON.stringify(snippet(val, 80))}`)
        else if (typeof val === 'number' || typeof val === 'boolean') pairs.push(`${k}=${val}`)
        else if (Array.isArray(val)) pairs.push(`${k}=[${val.length} item${val.length === 1 ? '' : 's'}]`)
        else pairs.push(`${k}={...}`)
        if (pairs.length >= 4) break
      }
      return snippet(pairs.join(', ') || JSON.stringify(parsed), n)
    }
    return snippet(String(v), n)
  }

  function flowSummary(ev) {
    const p = flowPayload(ev)
    if (!p) return ''
    const node = p.nodeId || p.node_id || p.visitKey || 'node'
    const kind = p.kind ? `${p.kind} ` : ''
    if (ev.type === 'flow.node.started' || p.status === 'running') {
      return `${node} · ${kind}started`
    }
    const dur = p.durationMs != null ? ` in ${p.durationMs}ms` : ''
    if (p.error) return `${node} · ${kind}failed${dur} — ${String(p.error)}`
    const out = shortValue(p.output, 220)
    const inp = shortValue(p.input, 180)
    if (out) return `${node} · ${kind}completed${dur} · output: ${out}`
    if (inp) return `${node} · ${kind}completed${dur} · input: ${inp}`
    return `${node} · ${kind}completed${dur}`
  }

  function flowDetail(ev) {
    const p = flowPayload(ev)
    if (!p) return ''
    try { return JSON.stringify(p, null, 2) } catch (_) { return String(ev.payload || '') }
  }

  function canExpandFlow(ev) {
    return !!flowPayload(ev)
  }

  function isDecisionEvent(ev) {
    const type = ev?.type || ''
    return type.startsWith('context.') || type.startsWith('llm.route.') || type.startsWith('lifecycle.')
  }

  function decisionDetail(ev) {
    try { return JSON.stringify(ev?.payload || {}, null, 2) } catch (_) { return String(ev?.payload || '') }
  }

  function normalizeRunHistory(rows) {
    return (rows || []).map(r => ({
      id: r.runId || r.sessionId || '',
      agentId: r.agentId || '',
      sessionId: r.sessionId || r.runId || '',
      trigger: r.trigger || r.channel || '',
      source: r.source || 'run-history',
      startedAt: r.startedAt || '',
      updatedAt: r.updatedAt || r.startedAt || '',
      status: r.status || (r.ok ? 'success' : 'unknown'),
      ok: !!r.ok,
      steps: r.steps || 0,
      output: r.output || r.error || '',
      error: r.error || '',
      failure: r.failure || null,
      deliveryStatus: r.deliveryStatus || '',
      deliveryChannel: r.deliveryChannel || '',
      deliveryTo: r.deliveryTo || '',
      deliveryError: r.deliveryError || '',
      hasBrowserTrace: !!r.hasBrowserTrace,
      browserEvents: r.browserEvents || 0,
    })).filter(r => r.id).sort((a, b) => new Date(b.updatedAt || b.startedAt || 0) - new Date(a.updatedAt || a.startedAt || 0))
  }

  function selectRun(run) {
    runFilter = run
    sessionFilter = ''
    typeFilter = 'all'
  }

  function clearRunFilter() {
    runFilter = null
  }

  function runMatchesEvent(run, ev) {
    if (!run || !ev) return true
    if (run.sessionId && ev.session_id !== run.sessionId) return false
    const t = new Date(ev.timestamp || 0).getTime()
    const start = new Date(run.startedAt || 0).getTime()
    const end = new Date(run.updatedAt || run.startedAt || 0).getTime()
    if (!Number.isFinite(t) || !Number.isFinite(start)) return true
    const pad = 1000
    return t >= start - pad && (!Number.isFinite(end) || end <= 0 || t <= end + pad)
  }

  function runTitle(run) {
    const status = run.status || 'unknown'
    const trigger = run.trigger || run.source || 'run'
    return `${status} · ${trigger} · ${fmtTime(run.startedAt)}`
  }

  function runPreview(run) {
    return snippet(run.failure?.summary || run.error || run.output || run.deliveryError || run.id, 110)
  }

  function partsText(p) {
    if (!p || !p.parts) return ''
    return p.parts.filter(x => x.type === 'text').map(x => x.text).join(' ')
  }

  // F-GUI-1 — When the runtime detects prompt-injection findings on a tool
  // result, engine.go:3560 wraps the payload as {tool_result:{…}, injection:{…}}.
  // Unwrap so downstream summary/chip/action code sees the plain ToolResult.
  function toolResultPayload(p) {
    if (!p) return {}
    if (p.tool_result && typeof p.tool_result === 'object') return p.tool_result
    return p
  }
  function injectionInfo(ev) {
    if (!ev) return null
    if (ev.type === 'tool.result') {
      const p = ev.payload || {}
      if (p && typeof p === 'object' && p.injection && p.tool_result) return p.injection
      return null
    }
    if (ev.type === 'injection.finding') return ev.payload || null
    return null
  }
  function intentInfo(ev) {
    if (!ev || ev.type !== 'intent.decision') return null
    return ev.payload || null
  }
  function hasSecuritySignal(ev) {
    const inj = injectionInfo(ev)
    if (inj && inj.max_severity && inj.max_severity !== 'none') return true
    const dec = intentInfo(ev)
    if (dec && dec.decision && dec.decision !== 'allow') return true
    // engine.go only emits intent.decision on Allow when
    // InjectionInfluenced=true; treat any emitted event as a signal.
    if (dec) return true
    return false
  }
  function severityClass(sev) {
    switch ((sev || '').toLowerCase()) {
      case 'high':   return 'danger'
      case 'medium': return 'warn'
      case 'low':    return 'warn'
      case 'info':   return 'info'
      case 'none':   return ''
      default:       return 'info'
    }
  }
  function decisionClass(dec) {
    switch ((dec || '').toLowerCase()) {
      case 'deny':   return 'danger'
      case 'prompt': return 'warn'
      case 'allow':  return 'info'
      default:       return 'info'
    }
  }
  function rowKey(ev, i) {
    return `${i}:${ev.timestamp || ''}:${ev.type || ''}`
  }
  function toggleExpanded(key) {
    if (expandedRows.has(key)) expandedRows.delete(key)
    else expandedRows.add(key)
    expandedRows = new Set(expandedRows)
  }

  function summary(ev) {
    const p = ev.payload || {}
    switch (ev.type) {
      case 'message.in': {
        const trig = p.metadata?.trigger || p.channel || 'message'
        return `run started (${trig}) — ${snippet(partsText(p), 100)}`
      }
      case 'llm.call':    return `LLM call → ${p.model || '?'} · turn ${p.turn ?? '?'}`
      case 'llm.result':  return `LLM result — ${p.output_tokens ?? 0} out / ${p.input_tokens ?? 0} in tokens · ${p.duration_ms ?? 0}ms${p.tool_calls ? ` · ${p.tool_calls} tool call(s)` : ''}`
      case 'llm.prompt': return `Prompt ${String(p.prompt_sha256 || '').slice(0, 12)} · ${p.message_count ?? 0} message(s) · about ${p.estimated_input_tokens ?? 0} tokens${p.content_captured ? ' · content captured' : ''}`
      case 'llm.completion': return `${p.outcome || 'completed'} · ${p.provider || p.requested_provider || '?'} / ${p.model || p.requested_model || '?'} · ${p.duration_ms ?? 0}ms`
      case 'llm.response.content': return `Streamed response ${String(p.response_sha256 || '').slice(0, 12)} · ${p.response_chars ?? 0} characters${p.content_captured ? ' · content captured' : ''}`
      case 'llm.route.decision': return `Route ${p.task_class || 'standard'} work across ${(p.candidates || []).length} candidate(s)`
      case 'llm.route.fallback': {
        const last = (p.attempts || []).at(-1) || {}
        return `${p.fallback_from || last.provider || 'provider'} failed (${p.fallback_reason || last.error_category || 'provider error'}); trying ${p.fallback_to || 'the next compatible model'}`
      }
      case 'llm.route.completed': return p.selected_provider
        ? `Selected ${p.selected_provider} / ${p.selected_model || 'default'}${p.fallbacks ? ` after ${p.fallbacks} fallback(s)` : ''}`
        : `No compatible route completed${p.attempts?.length ? ` after ${p.attempts.length} attempt(s)` : ''}`
      case 'context.prepared':
      case 'context.compacted': {
        const action = ev.type === 'context.compacted' ? `compacted ${p.dropped_messages || 0} old message(s)` : 'prepared'
        const sources = Object.entries(p.sources || {}).filter(([, enabled]) => enabled).map(([name]) => name.replaceAll('_', ' ')).join(', ')
        return `Context ${action} · about ${p.estimated_tokens_after ?? 0} / ${p.input_budget_tokens ?? '?'} tokens · ${sources || 'system prompt'}`
      }
      case 'lifecycle.run.before':
      case 'lifecycle.run.after':
      case 'lifecycle.context.after':
      case 'lifecycle.llm.before':
      case 'lifecycle.llm.after':
      case 'lifecycle.tool.before':
      case 'lifecycle.tool.after':
        return `${p.stage || ev.type.replace('lifecycle.', '')}${p.denied ? ` denied — ${p.reason || 'blocked by hook'}` : ' completed'}`
      case 'tool.call':   return `${p.name || 'tool'}(${snippet(JSON.stringify(p.arguments || {}), 80)})`
      case 'tool.result': {
        const tr = toolResultPayload(p)
        return `${tr.name || 'tool'} → ${snippet(tr.content, 100)}`
      }
      case 'reasoning.start':  return `reasoning loop started — ${p.strategy || '?'} · max ${p.max_steps ?? '?'} steps · ${p.tools ?? 0} tools`
      case 'reasoning.step':   return `${p.recovery ? 'recovery ' : ''}step ${p.index ?? '?'}: ${snippet(p.thought, 90)}${p.tool ? ` → ${p.tool}` : ''}`
      case 'reasoning.result': return `loop finished — ${p.steps ?? 0} step(s) · ${p.confident ? 'confident' : 'degraded / not confident'} · ${p.duration_ms ?? 0}ms`
      case 'flow.node.started':
      case 'flow.node':
        return flowSummary(ev)
      case 'message.out': return `reply — ${snippet(partsText(p), 120)}`
      case 'error':       return `[${p.stage || 'error'}] ${snippet(p.error, 160)}`
      case 'connected':   return String(ev.payload || 'stream connected')
      case 'injection.finding': {
        const sev = p.max_severity || 'info'
        return `prompt-injection ${sev} from ${p.source || 'unknown source'} — ${(p.findings || []).length} finding(s)`
      }
      case 'intent.decision': {
        return `intent ${p.decision || '?'} — ${p.tool || 'tool'} · ${snippet(p.reason, 120)}`
      }
      default:            return snippet(typeof ev.payload === 'string' ? ev.payload : JSON.stringify(ev.payload))
    }
  }

  function fmtTime(iso) { try { return new Date(iso).toLocaleTimeString() } catch { return '' } }

  function eventErrorText(ev) {
    const p = ev?.payload || {}
    return p.error || p.message || p.content || ev?.type || ''
  }

  function eventAgentId(ev) {
    return ev?.agent_id || (selectedId !== ALL_AGENTS ? selectedId : '')
  }

  function agentName(id) {
    const a = agents.find(x => x.id === id)
    return a?.name || id || ''
  }

  function canDebug(ev) {
    const p = ev?.payload || {}
    return !!ev?.session_id && (ev.type === 'error' || !!p.error || !!p.is_error)
  }

  function canLearn(ev) {
    return !!ev?.session_id && ev.type === 'message.out'
  }

  const FILTERS = {
    all:    () => true,
    run:    (t) => t === 'message.in' || t === 'message.out',
    llm:    (t) => t.startsWith('llm.'),
    tools:  (t) => t.startsWith('tool.'),
    errors: (t) => t === 'error',
    // F-GUI-1 — security is a shortcut for injection/intent-decision surfaces
    // regardless of source. Keeps the axis symmetrical with the other tabs
    // while the securityOnly toggle offers cross-type filtering.
    security: (t, ev) => t === 'injection.finding' || t === 'intent.decision' || hasSecuritySignal(ev),
    flow:     (t) => t.startsWith('flow.'),
    decisions: (t) => t.startsWith('context.') || t.startsWith('llm.route.') || t.startsWith('lifecycle.'),
  }
  $: filtered = events.filter(ev =>
    (FILTERS[typeFilter] || FILTERS.all)(ev.type || '', ev) &&
    (!securityOnly || hasSecuritySignal(ev) || ev.type === 'injection.finding' || ev.type === 'intent.decision') &&
    (!sessionFilter || ev.session_id === sessionFilter) &&
    (!runFilter || runMatchesEvent(runFilter, ev))
  )
</script>

<div class="page" class:activity-focused={activityFocused}>
  <div class="page-header">
    <h1>Runs</h1>
    <div class="header-actions">
      <button class="btn-secondary" class:active={watching} on:click={toggleWatch}>
        {watching ? '⏹ Stop watching' : '▶ Watch (2s)'}
      </button>
      <button class="btn-secondary" on:click={poll} disabled={!selectedId || loading}>↺ Refresh</button>
    </div>
        <TourButton />
    </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if replayMsg}<div class="banner ok">{replayMsg}</div>{/if}

  <div class="toolbar">
    <select bind:value={selectedId} on:change={() => selectAgent(selectedId)} class="agent-select">
      <option value={ALL_AGENTS}>All agents</option>
      {#if agents.length === 0}
        <option value="">No agents</option>
      {/if}
      {#each agents as a}
        <option value={a.id}>{a.name || a.id}</option>
      {/each}
    </select>
    <span class="toolbar-hint">Choose an agent, then select a run to inspect its activity.</span>
  </div>

  {#if sessionFilter}
    <div class="source-bar session-bar">
      <span class="source-label">Session</span>
      <code class="source-path">{sessionFilter}</code>
      <button class="row-action" on:click={() => sessionFilter = ''}>Show all runs</button>
    </div>
  {/if}

  {#if runningSessions.length > 0}
    <!-- E4c — Running now: live snapshot of in-flight sessions with a hung
         callout when a run has been silent past the tracker's threshold. -->
    <div class="running-strip" class:has-hung={hungCount > 0} aria-label="Running now">
      <div class="running-head">
        <span class="source-label">Running now</span>
        <span class="source-count">
          {runningSessions.length} session{runningSessions.length === 1 ? '' : 's'}
          {#if hungCount > 0}<span class="hung-pill">{hungCount} hung</span>{/if}
        </span>
      </div>
      <div class="running-cards">
        {#each runningSessions as sess (sess.session_id)}
          <button
            class="running-card"
            class:hung={sess.hung}
            title={sess.session_id}
            on:click={() => watchHungSession(sess)}>
            <div class="rc-head">
              <span class="rc-agent">{agentName(sess.agent_id) || sess.agent_id || '—'}</span>
              <span class="rc-timers">
                {fmtDuration(sess.elapsed_seconds)} in flight · silent {fmtDuration(sess.silent_seconds)}
              </span>
            </div>
            <div class="rc-last">last: {sess.last_event_type || 'unknown'}</div>
            {#if sess.hung}
              <div class="rc-reason">⚠ {sess.hung_reason}</div>
              {#if sess.hung_fix}<div class="rc-fix">→ {sess.hung_fix}</div>{/if}
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if runHistory.length > 0}
    <div class="run-strip" aria-label="Canonical run history">
      <div class="run-strip-head">
        <div class="run-strip-title">
          <span class="source-label">Runs</span>
          <span class="source-count">{runHistory.length} saved</span>
        </div>
        <div class="run-strip-actions">
          {#if runFilter}
            <button class="row-action" on:click={clearRunFilter}>Clear selection</button>
          {/if}
          <button class="row-action" on:click={toggleActivityFocus}>Expand activity</button>
        </div>
      </div>
      <div class="run-cards">
        {#each runHistory.slice(0, 18) as run (run.id)}
          <button
            class="run-card"
            class:on={runFilter?.id === run.id}
            class:failed={run.status === 'failed'}
            class:success={run.status === 'success'}
            title={run.id}
            on:click={() => selectRun(run)}
          >
            <span class="run-status">{run.status || 'unknown'}</span>
            <span class="run-time">{fmtTime(run.startedAt)}</span>
            <span class="run-trigger">{run.trigger || run.source || 'run'}</span>
            <span class="run-preview">{runPreview(run)}</span>
            {#if run.deliveryChannel || run.deliveryStatus}
              <span class="run-delivery">{run.deliveryChannel || 'delivery'} · {run.deliveryStatus || 'unknown'}</span>
            {/if}
            {#if run.hasBrowserTrace}
              <span class="run-browser">Browser trace · {run.browserEvents || 1} event{(run.browserEvents || 1) === 1 ? '' : 's'}</span>
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if runFilter}
    <div class="source-bar session-bar">
      <span class="source-label">Run</span>
      <code class="source-path">{runTitle(runFilter)}</code>
      <span class="source-count">session {runFilter.sessionId || '—'}</span>
      {#if runFilter.hasBrowserTrace}
        <button class="row-action browser" on:click={() => openRunBrowserTrace(runFilter)}>Browser trace</button>
      {/if}
    </div>
    <RunFailure failure={runFilter.failure} error={runFilter.status === 'failed' ? runFilter.error : ''} />
  {/if}

  <section class="activity-section" aria-label="Run activity">
    <div class="activity-head">
      <div class="activity-heading">
        <h2>Activity</h2>
        <span>{filtered.length} event{filtered.length === 1 ? '' : 's'}{runFilter ? ' in selected run' : ''}</span>
      </div>
      <div class="activity-actions">
        <label class="autoscroll"><input type="checkbox" bind:checked={autoScroll} /> Follow new events</label>
        <button class="focus-btn" class:on={activityFocused} on:click={toggleActivityFocus} aria-pressed={activityFocused}>
          {activityFocused ? '↙ Exit focus' : '↗ Expand activity'}
        </button>
      </div>
    </div>

    <div class="activity-filters" aria-label="Activity filters">
      <div class="chips primary-chips">
        {#each PRIMARY_FILTERS as f}
          <button class="chip" class:on={typeFilter === f} on:click={() => typeFilter = f}>{f}</button>
        {/each}
      </div>
      <button class="more-filters" class:on={showAdvancedFilters} on:click={() => showAdvancedFilters = !showAdvancedFilters} aria-expanded={showAdvancedFilters}>
        {showAdvancedFilters ? 'Fewer filters' : 'More filters'}
      </button>
      {#if typeFilter !== 'all'}
        <span class="filter-summary">Showing {typeFilter}</span>
      {/if}
    </div>

    {#if showAdvancedFilters}
      <div class="advanced-filters">
        <div class="chips">
          {#each ADVANCED_FILTERS as f}
            <button class="chip" class:on={typeFilter === f} on:click={() => typeFilter = f}>{f}</button>
          {/each}
        </div>
        <!-- F-GUI-1 — cross-cutting filter that leaves the type tabs alone but
             hides everything without an injection/intent security signal. -->
        <label class="sec-toggle" title="Hide events without an injection or intent-decision signal">
          <input type="checkbox" bind:checked={securityOnly} />
          Security findings only
        </label>
      </div>
    {/if}

    {#if path || selectedId === ALL_AGENTS}
      <details class="technical-source">
        <summary>Technical source</summary>
        <div>
          <span class="source-label">{path ? 'Log file' : 'Durable history'}</span>
          <code class="source-path">{path || 'All agents from the SQLite action log'}</code>
          <span class="source-count">{filtered.length} / {events.length} events</span>
        </div>
      </details>
    {/if}

    <div class="log-panel" bind:this={logEl} tabindex="-1">
    {#if loading && events.length === 0}
      <div class="empty">Loading…</div>
    {:else if filtered.length === 0}
      <div class="empty">
        {#if events.length === 0}
          No actions logged yet for <strong>{selectedId === ALL_AGENTS ? 'any agent' : (selectedId || 'this agent')}</strong>.
          <br>Trigger the agent from Automations, Studio, or Chat and its actions will appear here.
        {:else}
          No <strong>{typeFilter}</strong> events in the log.
        {/if}
      </div>
    {:else}
      {#each filtered as ev, i (i + (ev.timestamp || ''))}
        {@const m = meta(ev.type)}
        {@const inj = injectionInfo(ev)}
        {@const dec = intentInfo(ev)}
        {@const rk = rowKey(ev, i)}
        {@const expanded = expandedRows.has(rk)}
        <div class="row" class:is-error={ev.type === 'error' || ev.payload?.is_error} class:is-recovery={ev.payload?.recovery} class:is-degraded={ev.type === 'reasoning.result' && ev.payload?.confident === false}>
          <span class="t">{fmtTime(ev.timestamp)}</span>
          <span class="badge" style="color:{m.color}">{m.icon} {m.label}</span>
          <span class="sum">{summary(ev)}</span>
          {#if inj && inj.max_severity && inj.max_severity !== 'none'}
            <button
              class="sec-chip {severityClass(inj.max_severity)}"
              title="Prompt-injection findings — click to expand"
              on:click={() => toggleExpanded(rk)}
            >⚠ inject · {inj.max_severity}</button>
          {/if}
          {#if dec && dec.decision}
            <button
              class="sec-chip {decisionClass(dec.decision)}"
              title="Intent gate decision — click to expand"
              on:click={() => toggleExpanded(rk)}
            >⚑ intent · {dec.decision}</button>
          {/if}
          {#if selectedId === ALL_AGENTS && ev.agent_id}
            <span class="agent-pill" title={ev.agent_id}>{agentName(ev.agent_id)}</span>
          {/if}
          {#if ev.type === 'message.in' && ev.session_id}
            <button class="row-action" on:click={() => replayRun(ev)} disabled={!!replayingSession}>
              {replayingSession === ev.session_id ? 'Replaying…' : 'Replay'}
            </button>
          {/if}
          {#if canDebug(ev)}
            <button class="row-action debug" on:click={() => debugInStudio(ev)}>
              Debug in Studio
            </button>
          {/if}
          {#if canExpandFlow(ev)}
            <button class="row-action flow-detail-btn" on:click={() => toggleExpanded(rk)}>
              {expanded ? 'Hide details' : 'Details'}
            </button>
          {/if}
          {#if isDecisionEvent(ev)}
            <button class="row-action flow-detail-btn" on:click={() => toggleExpanded(rk)}>
              {expanded ? 'Hide decision' : 'Decision details'}
            </button>
          {/if}
          {#if isBrowserEvent(ev)}
            <button class="row-action browser" on:click={() => openBrowserTrace(ev)}>
              Browser trace
            </button>
          {/if}
          {#if canLearn(ev)}
            <button class="row-action learn" on:click={() => learnFromRun(ev)} disabled={!!learningSession}>
              {learningSession === ev.session_id ? 'Learning…' : 'Learn'}
            </button>
          {/if}
        </div>
        {#if expanded && (inj || dec)}
          <!-- F-GUI-1 — expanded security detail. Renders findings with pattern
               family, source tool, and content_location snippet; and the intent
               decision reasoning inline. Kept ASCII-only so it copies cleanly. -->
          <div class="row sec-detail">
            <span class="t"></span>
            <span class="badge sec-sum">Σ sec</span>
            <div class="sec-body">
              {#if inj}
                <div class="sec-hdr">Prompt-injection findings — max severity <strong>{inj.max_severity}</strong>{inj.findings?.length ? ` · ${inj.findings.length} finding(s)` : ''}</div>
                {#each (inj.findings || []) as f}
                  <div class="sec-line">
                    <span class="sec-pill {severityClass(f.severity)}">{f.severity}</span>
                    <span class="sec-family">{f.family || '?'}</span>
                    {#if f.pattern}<span class="sec-pattern">{f.pattern}</span>{/if}
                    {#if f.source}<span class="sec-source">from {f.source}</span>{/if}
                    {#if f.location}<span class="sec-location">@ {f.location}</span>{/if}
                    {#if f.snippet}<div class="sec-snippet">{f.snippet}</div>{/if}
                  </div>
                {/each}
              {/if}
              {#if dec}
                <div class="sec-hdr">Intent gate — <strong>{dec.decision}</strong> on {dec.tool || '?'}</div>
                <div class="sec-line">
                  <span class="sec-pill {decisionClass(dec.decision)}">{dec.decision}</span>
                  <span class="sec-reason">{dec.reason || ''}</span>
                </div>
                <div class="sec-line sec-flags">
                  <span>goal matched: <strong>{dec.goal_matched ? 'yes' : 'no'}</strong></span>
                  <span>injection influenced: <strong>{dec.injection_influenced ? 'yes' : 'no'}</strong></span>
                </div>
              {/if}
            </div>
          </div>
        {/if}
        {#if expanded && canExpandFlow(ev)}
          <div class="row flow-detail">
            <span class="t"></span>
            <span class="badge flow-sum">Σ flow</span>
            <pre class="flow-body">{flowDetail(ev)}</pre>
          </div>
        {/if}
        {#if expanded && isDecisionEvent(ev)}
          <div class="row flow-detail">
            <span class="t"></span>
            <span class="badge flow-sum">Σ trace</span>
            <pre class="flow-body">{decisionDetail(ev)}</pre>
          </div>
        {/if}
        {#if (ev.type === 'message.out' || ev.type === 'error') && ev.session_id}
          <div class="row metrics-row">
            <span class="t"></span>
            <span class="badge run-sum">Σ run</span>
            <RunMetrics sessionId={ev.session_id} agentId={eventAgentId(ev)} />
          </div>
        {/if}
      {/each}
    {/if}
    </div>
  </section>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; height: 100%; min-height: 0; overflow-y: auto; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .active { background: color-mix(in srgb, var(--sl-accent) 15%, transparent) !important; color: var(--sl-accent-hover) !important; border-color: color-mix(in srgb, var(--sl-accent) 40%, transparent) !important; }
  .banner { padding: .65rem 1rem; border-radius: 8px; font-size: .82rem; flex-shrink: 0; }
  .err  { background: color-mix(in srgb, var(--sl-danger) 10%, transparent); border: 1px solid color-mix(in srgb, var(--sl-danger) 30%, transparent); color: var(--sl-danger); }
  .ok   { background: color-mix(in srgb, var(--sl-success) 12%, transparent); border: 1px solid color-mix(in srgb, var(--sl-success) 35%, transparent); color: var(--sl-success); }

  .toolbar { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; flex-shrink: 0; }
  .agent-select { width: 240px; flex-shrink: 0; }
  .toolbar-hint { color: var(--sl-text-faint); font-size: .78rem; }
  .chips { display: flex; gap: .35rem; flex-wrap: wrap; }
  .chip {
    background: var(--sl-surface-muted); border: 1px solid var(--sl-line); color: var(--sl-text-faint);
    padding: .3rem .7rem; border-radius: 999px; font-size: .75rem; text-transform: capitalize;
  }
  .chip.on { background: color-mix(in srgb, var(--sl-accent) 15%, transparent); color: var(--sl-accent-hover); border-color: color-mix(in srgb, var(--sl-accent) 40%, transparent); }
  .autoscroll { display: flex; align-items: center; gap: .35rem; font-size: .75rem; color: var(--sl-text-faint); white-space: nowrap; }
  .autoscroll input { width: auto; }

  .source-bar {
    display: flex; align-items: center; gap: .75rem;
    padding: .5rem .85rem; background: var(--sl-surface-inset); border: 1px solid var(--sl-line);
    border-radius: 8px; flex-shrink: 0;
  }
  .source-label { font-size: .7rem; color: var(--sl-text-faint); text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .source-path  { font-size: .78rem; color: var(--sl-text-faint); flex: 1; word-break: break-all; }
  .source-count { font-size: .72rem; color: var(--sl-text-faint); flex-shrink: 0; }

  .run-strip {
    background: var(--sl-surface-inset); border: 1px solid var(--sl-line); border-radius: 10px;
    padding: .85rem; display: flex; flex-direction: column; gap: .7rem; flex-shrink: 0;
  }
  .run-strip-head { display: flex; align-items: center; justify-content: space-between; gap: .7rem; }
  .run-strip-title, .run-strip-actions { display: flex; align-items: center; gap: .65rem; }
  .run-cards {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(270px, 1fr));
    gap: .65rem; max-height: 310px; overflow: auto;
  }
  .run-card {
    text-align: left; border: 1px solid #242a47; background: var(--sl-surface-muted); color: var(--sl-text);
    border-radius: 9px; padding: .7rem .8rem; display: grid;
    grid-template-columns: 1fr auto; gap: .28rem .6rem; min-height: 92px;
  }
  .run-card:hover, .run-card.on { border-color: color-mix(in srgb, var(--sl-accent) 65%, transparent); background: color-mix(in srgb, var(--sl-accent) 12%, transparent); }
  .run-card.failed { border-left: 3px solid var(--sl-danger); }
  .run-card.success { border-left: 3px solid var(--sl-success); }
  .run-status { font-size: .68rem; text-transform: uppercase; letter-spacing: .05em; color: var(--sl-accent-hover); font-weight: 700; }
  .run-time { font-size: .66rem; color: var(--sl-text-faint); justify-self: end; }
  .run-trigger, .run-preview, .run-delivery, .run-browser { grid-column: 1 / -1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .run-trigger { color: var(--sl-text); font-size: .76rem; }
  .run-preview, .run-delivery { color: var(--sl-text-faint); font-size: .7rem; }
  .run-browser { color: var(--sl-info); font-size: .68rem; }

  /* E4c — Running now strip: live snapshot of in-flight sessions with hung callouts. */
  .running-strip {
    background: var(--sl-surface-inset); border: 1px solid var(--sl-line); border-radius: 10px;
    padding: .65rem; display: flex; flex-direction: column; gap: .55rem; flex-shrink: 0;
  }
  .running-strip.has-hung { border-color: color-mix(in srgb, var(--sl-danger) 40%, transparent); }
  .running-head { display: flex; align-items: center; gap: .7rem; }
  .hung-pill {
    display: inline-block; margin-left: .5rem;
    padding: .05rem .35rem; border-radius: 3px;
    background: color-mix(in srgb, var(--sl-danger) 15%, transparent); color: var(--sl-danger);
    font-size: .68rem; font-weight: 700;
  }
  .running-cards {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: .45rem; max-height: 240px; overflow: auto;
  }
  .running-card {
    text-align: left; border: 1px solid #242a47; background: var(--sl-surface-muted); color: var(--sl-text);
    border-radius: 8px; padding: .55rem .65rem;
    display: flex; flex-direction: column; gap: .2rem; cursor: pointer;
  }
  .running-card:hover { border-color: color-mix(in srgb, var(--sl-accent) 65%, transparent); background: color-mix(in srgb, var(--sl-accent) 12%, transparent); }
  .running-card.hung {
    border-color: color-mix(in srgb, var(--sl-danger) 55%, transparent); background: color-mix(in srgb, var(--sl-danger) 6%, transparent);
    border-left: 3px solid var(--sl-danger);
  }
  .rc-head { display: flex; justify-content: space-between; align-items: baseline; gap: .5rem; }
  .rc-agent { color: var(--sl-text); font-weight: 600; font-size: .78rem; }
  .rc-timers { color: var(--sl-text-faint); font-size: .66rem; }
  .rc-last { color: var(--sl-accent-hover); font-size: .68rem; text-transform: uppercase; letter-spacing: .04em; }
  .rc-reason { color: var(--sl-danger); font-size: .72rem; margin-top: .2rem; }
  .rc-fix    { color: #c8c7ff; font-size: .68rem; }

  .activity-section {
    flex: 1; min-height: min(440px, 55vh); display: flex; flex-direction: column; gap: .6rem;
    padding: .85rem; background: var(--sl-surface-inset); border: 1px solid var(--sl-line); border-radius: 10px;
  }
  .activity-head { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-shrink: 0; }
  .activity-heading { display: flex; align-items: baseline; gap: .65rem; }
  .activity-heading h2 { margin: 0; font-size: .95rem; color: var(--sl-text); }
  .activity-heading span { color: var(--sl-text-faint); font-size: .72rem; }
  .activity-actions { display: flex; align-items: center; gap: .65rem; }
  .focus-btn, .more-filters {
    background: #171b30; border: 1px solid var(--sl-line); color: #aeb4d0;
    border-radius: 7px; padding: .36rem .65rem; font-size: .72rem;
  }
  .focus-btn:hover, .focus-btn.on, .more-filters:hover, .more-filters.on {
    color: var(--sl-accent-hover); border-color: color-mix(in srgb, var(--sl-accent) 45%, transparent);
    background: color-mix(in srgb, var(--sl-accent) 12%, transparent);
  }
  .activity-filters { display: flex; align-items: center; gap: .55rem; flex-shrink: 0; }
  .filter-summary { color: var(--sl-text-faint); font-size: .7rem; }
  .advanced-filters {
    display: flex; align-items: center; gap: .65rem; padding: .55rem .65rem;
    background: var(--sl-surface-muted); border: 1px solid #242a47; border-radius: 8px; flex-shrink: 0;
  }
  .technical-source { flex-shrink: 0; color: var(--sl-text-faint); font-size: .72rem; }
  .technical-source summary { cursor: pointer; width: fit-content; }
  .technical-source > div {
    display: flex; align-items: center; gap: .65rem; margin-top: .45rem; padding: .45rem .6rem;
    background: var(--sl-surface-inset); border-radius: 7px;
  }
  .log-panel {
    flex: 1; min-height: 320px; overflow-y: auto;
    background: var(--sl-surface-inset); border: 1px solid var(--sl-line); border-radius: 10px;
    font-family: 'JetBrains Mono', 'Fira Code', monospace; font-size: .76rem; line-height: 1.5;
  }
  .log-panel:focus { outline: 1px solid color-mix(in srgb, var(--sl-accent) 45%, transparent); outline-offset: 1px; }

  .page.activity-focused { overflow: hidden; }
  .activity-focused > .page-header,
  .activity-focused > .toolbar,
  .activity-focused > .running-strip,
  .activity-focused > .run-strip,
  .activity-focused > .banner { display: none; }
  .activity-focused .activity-section { min-height: 0; height: 100%; }
  .activity-focused .log-panel { min-height: 0; }
  .empty { padding: 3rem 2rem; text-align: center; color: var(--sl-text-faint); line-height: 1.7; }

  .row {
    display: grid; grid-template-columns: 76px 86px minmax(0, 1fr) repeat(5, auto); gap: .6rem; align-items: start;
    padding: .3rem .85rem; border-bottom: 1px solid rgba(255,255,255,.03);
  }
  .row:hover { background: rgba(255,255,255,.03); }
  .row.is-error { background: color-mix(in srgb, var(--sl-danger) 6%, transparent); }
  .row.is-recovery { background: rgba(245,167,66,.055); }
  .row.is-degraded { background: rgba(245,167,66,.075); }
  .t     { color: var(--sl-text-faint); }
  .badge { font-weight: 700; font-size: .68rem; white-space: nowrap; }
  .sum   { color: var(--sl-text); white-space: pre-wrap; word-break: break-word; }
  .row-action {
    justify-self: end; align-self: center;
    background: color-mix(in srgb, var(--sl-accent) 12%, transparent); border: 1px solid color-mix(in srgb, var(--sl-accent) 35%, transparent);
    color: var(--sl-accent-ink); border-radius: 6px; padding: .18rem .48rem;
    font-family: inherit; font-size: .68rem;
  }
  .row-action.debug { background: color-mix(in srgb, var(--sl-success) 10%, transparent); border-color: color-mix(in srgb, var(--sl-success) 35%, transparent); color: #76d6a0; }
  .row-action.browser { background: rgba(64,196,255,.10); border-color: rgba(64,196,255,.35); color: var(--sl-info); }
  .row-action.learn { background: rgba(245,167,66,.10); border-color: rgba(245,167,66,.35); color: var(--sl-warning); }
  .row-action.flow-detail-btn { background: rgba(64,196,255,.10); border-color: rgba(64,196,255,.32); color: var(--sl-info); }
  .row-action:disabled { opacity: .55; cursor: wait; }
  .agent-pill {
    justify-self: end; align-self: center; max-width: 180px; overflow: hidden; text-overflow: ellipsis;
    white-space: nowrap; border: 1px solid color-mix(in srgb, var(--sl-accent) 28%, transparent); background: color-mix(in srgb, var(--sl-accent) 10%, transparent);
    color: var(--sl-accent-ink); border-radius: 999px; padding: .14rem .45rem; font-size: .66rem;
  }

  .metrics-row { opacity: .85; }
  .run-sum { color: var(--sl-text-faint); }

  /* F-GUI-1 — Cohort F security signals. Colors match the deployment-profile
     scheme used in Dashboard: info=blue, warn=amber, danger=red. */
  .sec-toggle {
    display: flex; align-items: center; gap: .35rem;
    font-size: .72rem; color: var(--sl-text-faint);
    padding: .3rem .6rem;
    background: var(--sl-surface-muted); border: 1px solid #242a47; border-radius: 999px;
    cursor: pointer;
  }
  .sec-toggle input { width: auto; margin: 0; }
  .sec-chip {
    justify-self: end; align-self: center;
    font-size: .65rem; font-family: inherit; font-weight: 700;
    border-radius: 999px; padding: .14rem .5rem; white-space: nowrap;
    background: color-mix(in srgb, var(--sl-info) 12%, transparent); border: 1px solid color-mix(in srgb, var(--sl-info) 35%, transparent); color: var(--sl-info);
    cursor: pointer;
  }
  .sec-chip.info   { background: color-mix(in srgb, var(--sl-info) 12%, transparent); border-color: color-mix(in srgb, var(--sl-info) 35%, transparent); color: var(--sl-info); }
  .sec-chip.warn   { background: rgba(245,167,66,.15);  border-color: rgba(245,167,66,.4);   color: var(--sl-warning); }
  .sec-chip.danger { background: color-mix(in srgb, var(--sl-danger) 15%, transparent);   border-color: color-mix(in srgb, var(--sl-danger) 40%, transparent);    color: var(--sl-danger); }
  .sec-detail { background: color-mix(in srgb, var(--sl-info) 4%, transparent); }
  .sec-sum { color: var(--sl-info); }
  .sec-body {
    grid-column: 3 / -1;
    display: flex; flex-direction: column; gap: .35rem;
    color: var(--sl-text); font-size: .74rem;
  }
  .sec-hdr { color: var(--sl-accent-ink); font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; }
  .sec-line { display: flex; flex-wrap: wrap; gap: .4rem; align-items: center; }
  .sec-flags { color: var(--sl-text-faint); font-size: .7rem; }
  .sec-pill {
    padding: .06rem .4rem; border-radius: 3px; font-size: .66rem; font-weight: 700;
    text-transform: uppercase; letter-spacing: .04em;
    background: color-mix(in srgb, var(--sl-info) 15%, transparent); color: var(--sl-info);
  }
  .sec-pill.info   { background: color-mix(in srgb, var(--sl-info) 15%, transparent); color: var(--sl-info); }
  .sec-pill.warn   { background: rgba(245,167,66,.18);  color: var(--sl-warning); }
  .sec-pill.danger { background: color-mix(in srgb, var(--sl-danger) 18%, transparent);   color: var(--sl-danger); }
  .sec-family  { color: var(--sl-text); font-weight: 600; }
  .sec-pattern { color: var(--sl-accent-ink); }
  .sec-source, .sec-location { color: var(--sl-text-faint); font-size: .68rem; }
  .sec-reason  { color: var(--sl-text); }
  .sec-snippet {
    width: 100%; padding: .3rem .5rem;
    background: var(--sl-surface-inset); border: 1px solid var(--sl-line); border-radius: 5px;
    color: var(--sl-text-faint); font-family: 'JetBrains Mono', 'Fira Code', monospace;
    font-size: .68rem; white-space: pre-wrap; word-break: break-word;
    margin-top: .1rem;
  }

  .flow-detail { background: rgba(64,196,255,.035); }
  .flow-sum { color: var(--sl-info); }
  .flow-body {
    grid-column: 3 / -1;
    margin: 0;
    max-height: 360px;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    word-break: break-word;
    background: var(--sl-surface-inset);
    border: 1px solid var(--sl-line);
    border-radius: 7px;
    color: var(--sl-text-dim);
    padding: .65rem .75rem;
    font-family: 'JetBrains Mono', 'Fira Code', monospace;
    font-size: .72rem;
    line-height: 1.5;
  }

  @media (max-width: 900px) {
    .page { padding: 1rem; }
    .run-cards { grid-template-columns: 1fr; max-height: 360px; }
    .activity-head, .activity-actions, .advanced-filters { align-items: flex-start; }
    .activity-head { flex-direction: column; }
    .activity-actions { width: 100%; justify-content: space-between; }
    .advanced-filters { flex-direction: column; }
    .row { grid-template-columns: 68px 72px minmax(0, 1fr); }
    .row-action, .sec-chip, .agent-pill { grid-column: 3; justify-self: start; }
  }
</style>
