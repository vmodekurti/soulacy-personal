<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { activityAgent } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import { catchupLabel, catchupTitle } from '../lib/schedutils.js'

  function watchAgent(id, sessionId = '') {
    activityAgent.set(id)
    if (sessionId) {
      const q = new URLSearchParams({ agent_id: id, session_id: sessionId })
      location.hash = `#activity?${q.toString()}`
    } else {
      location.hash = 'activity'
    }
  }

  const IMMINENT_MS = 15 * 60 * 1000  // a scheduled run within 15 min is "about to run"

  let schedule    = []
  let agents      = []
  let channels    = []
  let loading     = true
  let error       = ''
  let notice      = ''
  let busy        = {}      // agentId → true while a manual run request is in flight

  // Live status (polled)
  let running     = {}      // agentId → ISO start time (currently executing)
  let nextRuns    = {}      // agentId → ISO next scheduled fire
  let backfills   = {}      // agentId → {missed_at, replayed_at, cron, window, late_by}
  let now         = Date.now()
  let promptedFires = new Set()  // "id@nextISO" already auto-prompted
  let recentRuns = []
  let recentLimit = 25
  let recentLoading = false
  let recentError = ''
  let recentRunsExpanded = true

  // Modals
  let editing  = null
  let editCron = ''
  let editEnabled = false
  let editOutputChannel = ''
  let editOutputTo = ''
  let editOutputBotName = ''
  let editOutputTemplate = ''
  let editRunMissedOnStartup = false
  let editMissedStartupWindow = ''
  let saving   = false
  let runPrompt = null      // { agent, next, auto }

  // History panel
  let historyAgent   = null   // agent whose history is open
  let historyRuns    = []     // [{id, sessionId, startTime, status, output}]
  let historyLoading = false
  let historyError   = ''
  let historySourceSummary = ''
  let expandedRuns   = {}     // run id → true (output expanded)

  function partsText(payload) {
    return (payload?.parts || []).filter(x => x.type === 'text').map(x => x.text).join('\n')
  }

  function groupRuns(events) {
    const ordered = events.slice().sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp))
    const runs = []
    const activeBySession = {}
    const startTypes = new Set(['message.in'])

    for (const ev of ordered) {
      const sid = ev.session_id || 'unknown'
      if (!activeBySession[sid] || startTypes.has(ev.type)) {
        const run = {
          id: `${sid}:${ev.timestamp || runs.length}:${runs.length}`,
          sessionId: sid,
          events: [],
        }
        runs.push(run)
        activeBySession[sid] = run
      }
      activeBySession[sid].events.push(ev)
    }

    return runs.map((run) => {
      const sid = run.sessionId
      const evs = run.events
      // Event timestamp field is "timestamp" (not "created_at") per message.Event struct
      const sorted = evs.slice().sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp))
      const outEv    = sorted.find(e => e.type === 'message.out')
      const errEv    = sorted.find(e => e.type === 'error')
      const inEv     = sorted.find(e => e.type === 'message.in')
      const reasonEv = sorted.find(e => e.type === 'reasoning.result')
      const deliveryEv = sorted.slice().reverse().find(e => e.type === 'schedule.output')
      const delivery = deliveryEv?.payload || {}
      // A run is failed if any tool returned is_error:true, even when the LLM
      // still produced a message.out summarising the failure.
      const toolFailed = sorted.some(e => e.type === 'tool.result' && e.payload?.is_error === true)
      const recovered = sorted.some(e => e.type === 'reasoning.step' && e.payload?.recovery === true)

      let status = 'unknown'
      if (outEv && !toolFailed && !errEv) status = 'success'
      else if (toolFailed || errEv)        status = 'failed'
      else if (outEv)                      status = 'success'
      if (status === 'success' && (reasonEv?.payload?.confident === false || recovered)) status = 'degraded'

      let output = ''
      if (outEv) {
        output = partsText(outEv.payload) || JSON.stringify(outEv.payload)
      }
      if (errEv) {
        const ep = errEv.payload
        const errText = typeof ep === 'string' ? ep : (ep?.message || ep?.error || JSON.stringify(ep))
        output = output ? output + '\n\n⚠ Error: ' + errText : '⚠ Error: ' + errText
        status = 'failed'
      }

      const channel = inEv?.payload?.channel || delivery.trigger || ''
      const startTime = (inEv || sorted[0])?.timestamp

      const agentId = sorted.find(e => e.agent_id)?.agent_id || ''
      const deliveryStatus = deliveryEv ? (delivery.delivered === true ? 'delivered' : 'failed') : ''
      const deliveryError = delivery.detail || delivery.reason || ''

      return {
        id: run.id,
        sessionId: sid,
        agentId,
        startTime,
        status,
        output,
        channel,
        source: 'action-log',
        deliveryStatus,
        deliveryChannel: delivery.channel || '',
        deliveryTo: delivery.to || '',
        deliveryError,
      }
    }).sort((a, b) => new Date(b.startTime) - new Date(a.startTime))
  }

  function runKey(run) {
    return run.id || `${run.sessionId || ''}:${run.startTime || ''}:${run.source || ''}`
  }

  function mergeHistoryRuns(primary, fallback) {
    const merged = []
    const byKey = new Map()
    for (const run of [...primary, ...fallback]) {
      const key = runKey(run)
      if (!key) continue
      const existing = byKey.get(key)
      if (!existing) {
        const copy = { ...run, id: run.id || key }
        byKey.set(key, copy)
        merged.push(copy)
        continue
      }
      existing.output ||= run.output || ''
      existing.channel ||= run.channel || ''
      existing.source = existing.source === run.source ? existing.source : `${existing.source || 'run-history'} + ${run.source || 'action-log'}`
      existing.steps ||= run.steps || 0
      existing.deliveryStatus ||= run.deliveryStatus || ''
      existing.deliveryChannel ||= run.deliveryChannel || ''
      existing.deliveryTo ||= run.deliveryTo || ''
      existing.deliveryError ||= run.deliveryError || ''
      if (existing.status === 'unknown' && run.status) existing.status = run.status
      if (run.status === 'failed') existing.status = 'failed'
      if (!existing.startTime || (run.startTime && new Date(run.startTime) < new Date(existing.startTime))) existing.startTime = run.startTime
    }
    return merged.sort((a, b) => new Date(b.startTime || 0) - new Date(a.startTime || 0))
  }

  function normalizeLedgerRun(r) {
    return {
      id: r.id || r.runId || `${r.sessionId || ''}:${r.startedAt || ''}`,
      sessionId: r.sessionId || r.runId || '',
      agentId: r.agentId || '',
      agentName: r.agentName || r.agentId || '',
      startTime: r.startedAt,
      updatedAt: r.updatedAt,
      status: r.status || (r.ok ? 'success' : 'unknown'),
      output: r.output || r.error || '',
      channel: r.channel || r.trigger || '',
      source: r.source || 'action-log',
      steps: r.steps || 0,
      eventCount: r.eventCount || 0,
      durationMs: r.durationMs || 0,
      deliveryStatus: r.deliveryStatus || '',
      deliveryChannel: r.deliveryChannel || '',
      deliveryTo: r.deliveryTo || '',
      deliveryError: r.deliveryError || '',
    }
  }

  async function openHistory(a) {
    historyAgent   = a
    historyRuns    = []
    historyError   = ''
    historySourceSummary = ''
    expandedRuns   = {}
    historyLoading = true
    try {
      const ledger = await api.runs.ledger({ agentId: a.id, limit: 100, eventLimit: 50000 }).catch(() => ({ runs: null }))
      if (Array.isArray(ledger.runs)) {
        historyRuns = ledger.runs.map(normalizeLedgerRun)
        const source = ledger.source ? ` · source: ${ledger.source}` : ''
        const trunc = ledger.event_truncated ? ` · event query hit ${ledger.event_limit || 'the'} cap; older/noisier runs may be hidden` : ''
        historySourceSummary = `${historyRuns.length} shown · ${ledger.event_count || 0} events scanned · unified durable run ledger${source}${trunc}`
        return
      }
      const hist = await api.studio.runHistory(a.id).catch(() => ({ runs: [] }))
      const retainedRuns = (hist.runs || []).map((r) => ({
        id: r.runId || r.sessionId,
        sessionId: r.sessionId || r.runId,
        startTime: r.startedAt,
        status: r.status || (r.ok ? 'success' : 'failed'),
        output: r.output || r.error || '',
        channel: r.trigger || '',
        steps: r.steps || 0,
        source: r.source || 'run-history',
        deliveryStatus: r.deliveryStatus || '',
        deliveryChannel: r.deliveryChannel || '',
        deliveryTo: r.deliveryTo || '',
        deliveryError: r.deliveryError || '',
      }))
      const actions = await api.agents.actions(a.id, 10000, 'message.in,message.out,error,tool.result,reasoning.step,reasoning.result,schedule.output', { durable: true }).catch(() => ({ events: [] }))
      const actionRuns = groupRuns(actions.events || [])
      historyRuns = mergeHistoryRuns(retainedRuns, actionRuns)
      historySourceSummary = `${historyRuns.length} shown · ${retainedRuns.length} retained · ${actionRuns.length} reconstructed from action log`
    } catch (e) {
      historyError = e.message
    } finally {
      historyLoading = false
    }
  }

  function closeHistory() { historyAgent = null }
  function toggleRun(runId) { expandedRuns = { ...expandedRuns, [runId]: !expandedRuns[runId] } }

  let statusTimer, tick

  async function loadRecentRuns() {
    recentLoading = true
    recentError = ''
    try {
      const res = await api.runs.ledger({ limit: recentLimit, eventLimit: 50000 })
      recentRuns = (res.runs || []).map(normalizeLedgerRun)
      if (res.event_truncated) {
        recentError = `Recent run list scanned ${res.event_limit || 50000} events and may be truncated. Use Activity for exact log tails.`
      }
    } catch (e) {
      const ledgerError = e.message
      const res = await api.runs.events({
        limit: 5000,
        types: 'message.in,message.out,error,tool.result,reasoning.step,reasoning.result,schedule.output',
      }).catch(() => ({ events: [] }))
      recentRuns = groupRuns(res.events || []).slice(0, recentLimit)
      recentError = recentRuns.length ? '' : ledgerError
    } finally {
      recentLoading = false
    }
  }

  async function load() {
    loading = true
    error   = ''
    try {
      const [schedRes, agentRes, channelRes] = await Promise.all([
        api.schedule.list(),
        api.agents.list(),
        api.channels.list().catch(() => ({ channels })),
      ])
      schedule = schedRes.schedule || []
      agents   = agentRes.agents   || []
      channels = channelRes.channels || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
    await refreshStatus()
    await loadRecentRuns()
  }

  async function refreshStatus() {
    try {
      const s = await api.schedule.status()
      running   = s.running   || {}
      nextRuns  = s.next      || {}
      backfills = s.backfills || {}
    } catch (e) {
      // keep last known status on transient errors
    }
  }

  onMount(() => {
    load()
    statusTimer = setInterval(refreshStatus, 5000)
    tick = setInterval(() => { now = Date.now(); checkAutoPrompt() }, 1000)
  })
  onDestroy(() => { clearInterval(statusTimer); clearInterval(tick) })

  $: cronAgents = agents.filter(a => a.trigger === 'cron')
  $: outputBotOptions = buildOutputBotOptions(channels)

  // Reactive per-agent status. This $: block references running/nextRuns/now
  // DIRECTLY, so Svelte re-renders the table whenever any of them change.
  // (Calling isRunning(a.id) in markup wouldn't track `running`, because Svelte
  // doesn't look inside function bodies for dependencies.)
  $: statusById = (() => {
    const m = {}
    for (const a of cronAgents) {
      const nx = nextRuns[a.id]
      const ms = nx ? new Date(nx).getTime() - now : null
      m[a.id] = {
        running:  !!running[a.id],
        imminent: ms != null && ms > 0 && ms <= IMMINENT_MS,
        ms,
        hasNext:  !!nx,
      }
    }
    return m
  })()

  // --- status helpers (used by event handlers, where reactivity isn't needed) ---
  const isRunning  = (id) => !!running[id]
  function msUntil(id) {
    const nx = nextRuns[id]
    if (!nx) return null
    return new Date(nx).getTime() - now
  }
  function isImminent(id) {
    const m = msUntil(id)
    return m != null && m > 0 && m <= IMMINENT_MS
  }
  function fmtCountdown(ms) {
    if (ms == null || ms < 0) return ''
    const s = Math.round(ms / 1000)
    const m = Math.floor(s / 60)
    const sec = s % 60
    if (m >= 60) { const h = Math.floor(m / 60); return `${h}h ${m % 60}m` }
    return m > 0 ? `${m}m ${sec}s` : `${sec}s`
  }
  function fmtTime(iso) {
    return iso ? new Date(iso).toLocaleString() : '—'
  }

  // Auto-prompt when an enabled cron job enters the imminence window (once per fire).
  function checkAutoPrompt() {
    if (runPrompt || editing) return  // never stack modals
    for (const a of cronAgents) {
      if (!a.enabled || isRunning(a.id)) continue
      const nx = nextRuns[a.id]
      if (!nx) continue
      const m = new Date(nx).getTime() - now
      if (m > 0 && m <= IMMINENT_MS) {
        const key = a.id + '@' + nx
        if (!promptedFires.has(key)) {
          promptedFires.add(key)
          runPrompt = { agent: a, next: nx, auto: true }
          return
        }
      }
    }
  }

  function setBusy(id, v) { busy = { ...busy, [id]: v } }

  // --- run ---
  function onRunClick(a) {
    if (isRunning(a.id) || busy[a.id]) return
    if (isImminent(a.id)) {
      // mark this fire as handled so the auto-popup won't also fire for it
      promptedFires.add(a.id + '@' + nextRuns[a.id])
      runPrompt = { agent: a, next: nextRuns[a.id], auto: false }
      return
    }
    doRun(a.id)
  }

  async function doRun(id) {
    setBusy(id, true)
    error = ''; notice = ''
    // Optimistically mark running so the indicator shows instantly, even for
    // fast runs that finish before the next status poll.
    running = { ...running, [id]: new Date().toISOString() }
    try {
      const res = await api.agents.trigger(id)
      const deliveryNote = res?.delivered
        ? ' 📤 Result was sent to the configured output channel.'
        : ' (Result shown here only — no output channel resolved; configure one under Edit → Scheduled output.)'
      notice = `▶ ${id} ran. ` + (res?.result ? `Result: ${truncate(res.result, 160)}` : 'Done.') + deliveryNote
      await loadRecentRuns()
    } catch (e) {
      error = e.message   // includes "agent is already running" (409)
    } finally {
      setBusy(id, false)
      refreshStatus()     // reconcile with server (clears running when finished)
    }
  }

  function promptRunNow() {
    const id = runPrompt.agent.id
    runPrompt = null
    doRun(id)
  }
  function promptWait() { runPrompt = null }

  async function testOutput(a) {
    if (!a?.id || busy[a.id]) return
    setBusy(a.id, true)
    error = ''; notice = ''
    try {
      const res = await api.agents.testScheduleOutput(a.id)
      notice = `Sent scheduled-output test for "${a.id}" via ${res.channel}.`
      await loadRecentRuns()
    } catch (e) {
      error = e.message
    } finally {
      setBusy(a.id, false)
    }
  }

  // --- clone / delete / edit ---
  async function clone(agentId) {
    setBusy(agentId, true); error = ''; notice = ''
    try {
      const res = await api.agents.clone(agentId)
      notice = `Cloned to "${res.id}" (created disabled).`
      await load()
    } catch (e) { error = e.message } finally { setBusy(agentId, false) }
  }

  async function remove(agentId) {
    if (isRunning(agentId)) { error = `"${agentId}" is currently running — wait for it to finish before deleting.`; return }
    if (!confirm(`Delete agent "${agentId}"? This removes its SOUL.yaml from disk.`)) return
    setBusy(agentId, true); error = ''; notice = ''
    try {
      await api.agents.delete(agentId)
      notice = `Deleted "${agentId}".`
      await load()
    } catch (e) { error = e.message } finally { setBusy(agentId, false) }
  }

  function openEdit(a) {
    editing     = JSON.parse(JSON.stringify(a))
    editCron    = a.schedule?.cron || ''
    editEnabled = !!a.enabled
    editOutputChannel = a.schedule?.output?.channel || ''
    editOutputTo = a.schedule?.output?.to || ''
    editOutputBotName = a.schedule?.output?.bot_name || botNameForChannel(editOutputChannel)
    editOutputTemplate = a.schedule?.output?.template || ''
    editRunMissedOnStartup = !!a.schedule?.run_missed_on_startup
    editMissedStartupWindow = a.schedule?.missed_startup_window || '24h'
  }
  function closeEdit() { editing = null }

  async function saveEdit() {
    if (!editing) return
    saving = true; error = ''; notice = ''
    try {
      if (!editing.schedule) editing.schedule = {}
      editing.schedule.cron = editCron.trim()
      editing.schedule.run_missed_on_startup = editRunMissedOnStartup
      if (editRunMissedOnStartup) {
        editing.schedule.missed_startup_window = editMissedStartupWindow.trim() || '24h'
      } else {
        delete editing.schedule.missed_startup_window
      }
      if (editOutputChannel.trim()) {
        if (!editOutputTo.trim()) {
          throw new Error('Destination ID is required when scheduled output is enabled.')
        }
        editing.schedule.output = {
          channel: editOutputChannel.trim(),
          to: editOutputTo.trim(),
          bot_name: editOutputBotName.trim(),
          template: editOutputTemplate.trim(),
        }
      } else if (editing.schedule.output) {
        delete editing.schedule.output
      }
      editing.enabled = editEnabled
      await api.agents.update(editing.id, editing)
      notice = `Saved "${editing.id}".`
      closeEdit()
      await load()
    } catch (e) { error = e.message } finally { saving = false }
  }

  function agentName(id) {
    const a = agents.find(a => a.id === id)
    return a?.name || id
  }
  function recentAgentName(run) {
    return agentName(run.agentId || '')
  }
  // hasDefaultSender mirrors Channels.svelte: a channel-level outbound sender
  // (base Telegram token, or an explicit default destination) that cron jobs use
  // when they don't target an agent-specific bot.
  function hasDefaultSender(ch) {
    return !!(ch?.settings?.token || ch?.settings?.default_output_to || ch?.settings?.bot_name)
  }

  function buildOutputBotOptions(list) {
    const rows = []
    const seen = new Set()
    const pushOpt = (o) => { if (!seen.has(o.channel)) { seen.add(o.channel); rows.push(o) } }

    for (const ch of list || []) {
      if (!ch || ch.id === 'http') continue

      // Channel-level DEFAULT OUTBOUND sender — the target for cron/scheduled
      // output that isn't routed through a per-agent bot. This was previously
      // omitted, so Telegram's default outbound never appeared as an option.
      if (hasDefaultSender(ch) && (ch.id === 'telegram' || ch.settings?.default_output_to)) {
        const name = ch.settings?.bot_name || ch.name || ch.id
        pushOpt({
          channel: ch.id,
          bot_name: name,
          agent_id: '',
          connected: !!ch.status?.connected,
          label: `${name} (${ch.id} · default outbound)`,
        })
      }

      const bots = ch.bots || []
      for (const bot of bots) {
        const adapterID = bot._adapter_id || ch.id
        pushOpt({
          channel: adapterID,
          bot_name: bot.bot_name || adapterID,
          agent_id: bot.agent_id || '',
          connected: !!bot._connected,
          label: `${bot.bot_name || adapterID} (${adapterID}${bot.agent_id ? ' → ' + bot.agent_id : ''})`,
        })
      }

      if (!bots.length && (ch.settings?.agent_id || ch.id === 'whatsapp')) {
        const name = ch.settings?.bot_name || ch.name || ch.id
        pushOpt({
          channel: ch.id,
          bot_name: name,
          agent_id: ch.settings?.agent_id || '',
          connected: !!ch.status?.connected,
          label: `${name} (${ch.id}${ch.settings?.agent_id ? ' → ' + ch.settings.agent_id : ''})`,
        })
      }
    }
    return rows
  }
  function botNameForChannel(channelID) {
    return outputBotOptions.find(o => o.channel === channelID)?.bot_name || ''
  }
  function selectOutputChannel(channelID) {
    editOutputChannel = channelID
    editOutputBotName = botNameForChannel(channelID)
  }
  function outputSummary(a) {
    const out = a.schedule?.output
    if (!out?.channel) return null
    const name = out.bot_name || botNameForChannel(out.channel) || out.channel
    return { name, channel: out.channel, to: out.to || '' }
  }
  function truncate(s, n) { return s.length > n ? s.slice(0, n) + '…' : s }
</script>

<div class="page">
  <div class="page-header">
    <h1>Automations</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
        <TourButton />
    </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}

  <!-- Active scheduled jobs from API -->
  <section class="section">
    <div class="section-hdr">
      <span>Active automations</span>
      <span class="pill">{schedule.length} entr{schedule.length === 1 ? 'y' : 'ies'}</span>
    </div>

    {#if loading}
      <div class="empty">Loading…</div>
    {:else if schedule.length === 0}
      <div class="empty">No active automations. Enable a cron agent below to register it with the scheduler.</div>
    {:else}
      <table class="tbl">
        <thead>
          <tr><th>Agent</th><th>Next run</th><th>Last run</th><th>Missed runs</th></tr>
        </thead>
        <tbody>
          {#each schedule as entry}
            <tr>
              <td class="td-name">
                {agentName(entry.agent_id)}
                {#if backfills[entry.agent_id]}
                  <!-- E4b — auto-replayed chip: surfaces a startup catch-up so the operator can
                       spot "wait, this shouldn't have fired at 03:04" without spelunking logs. -->
                  <span
                    class="backfill-chip"
                    title={`Missed a fire at ${new Date(backfills[entry.agent_id].missed_at).toLocaleString()}; replayed at boot on ${new Date(backfills[entry.agent_id].replayed_at).toLocaleString()} (${backfills[entry.agent_id].late_by} late). Within the ${backfills[entry.agent_id].window} missed-startup window.`}>
                    ⟳ auto-replayed
                  </span>
                {/if}
              </td>
              <td class="td-hint">{entry.next ? new Date(entry.next).toLocaleString() : '—'}</td>
              <td class="td-hint">{entry.prev && !entry.prev.startsWith('0001') ? new Date(entry.prev).toLocaleString() : '—'}</td>
              <td class="td-hint">
                <span class="catchup" class:on={entry.catch_up} title={catchupTitle(entry)}>
                  {entry.catch_up ? '⟳' : '—'} {catchupLabel(entry)}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="field-help missed-help">
        <strong>Missed runs:</strong> if the gateway is down at an agent's scheduled
        time, agents with <code>run_missed_on_startup</code> run the <em>latest</em>
        missed fire once at startup (within their window; default 24h). Older missed
        fires are never replayed, and completed fires are remembered across restarts —
        no duplicates. All other agents simply skip missed fires.
      </p>
    {/if}
  </section>

  <section class="section">
    <div class="section-hdr">
      <button
        class="section-toggle"
        type="button"
        aria-expanded={recentRunsExpanded}
        aria-controls="recent-runs-content"
        on:click={() => recentRunsExpanded = !recentRunsExpanded}
        title={recentRunsExpanded ? 'Collapse recent runs' : 'Expand recent runs'}
      >
        <span class="section-chevron" aria-hidden="true">{recentRunsExpanded ? '▾' : '▸'}</span>
        <span>Recent runs</span>
        <span class="pill">{recentRuns.length}</span>
      </button>
      <label class="history-depth" title="How many unified runs to show. Includes manual, chat/channel, cron, and scheduled-output runs when durable history is available.">
        <span>Show</span>
        <select bind:value={recentLimit} on:change={loadRecentRuns} disabled={recentLoading}
                title="How many unified runs to show. Includes manual, chat/channel, cron, and scheduled-output runs when durable history is available.">
          <option value={12}>12</option>
          <option value={25}>25</option>
          <option value={50}>50</option>
          <option value={100}>100</option>
        </select>
      </label>
      <button class="btn-secondary xs hdr-action" on:click={loadRecentRuns} disabled={recentLoading}>↺ Refresh</button>
    </div>
    {#if recentRunsExpanded}
      <div id="recent-runs-content">
        {#if recentLoading}
          <div class="empty">Loading run history…</div>
        {:else if recentError}
          <div class="empty err">{recentError}</div>
        {:else if recentRuns.length === 0}
          <div class="empty">No recent runs recorded in durable history yet.</div>
        {:else}
          <table class="tbl">
            <thead>
              <tr><th>Agent</th><th>Started</th><th>Trigger</th><th>Status</th><th>Delivery</th><th>Source</th><th class="td-action">Actions</th></tr>
            </thead>
            <tbody>
              {#each recentRuns as run (run.id)}
                <tr class:row-failed={run.status === 'failed'} class:row-degraded={run.status === 'degraded'}>
                  <td class="td-name">{recentAgentName(run) || 'Unknown agent'}</td>
                  <td class="td-hint">{run.startTime ? new Date(run.startTime).toLocaleString() : '—'}</td>
                  <td class="td-hint">{run.channel || '—'}</td>
                  <td>
                    <span class="run-badge inline" class:badge-ok={run.status === 'success'} class:badge-fail={run.status === 'failed'} class:badge-warn={run.status === 'degraded'} class:badge-unk={run.status === 'unknown'}>
                      {run.status === 'success' ? 'success' : run.status === 'failed' ? 'failed' : run.status === 'degraded' ? 'degraded' : 'unknown'}
                    </span>
                  </td>
                  <td class="td-hint">
                    {#if run.deliveryStatus}
                      <span class="delivery-mini" class:delivery-fail={run.deliveryStatus === 'failed'} title={run.deliveryError || ''}>
                        {run.deliveryStatus === 'delivered' ? 'delivered' : 'failed'}
                        {#if run.deliveryChannel} · {run.deliveryChannel}{/if}
                      </span>
                    {:else}
                      —
                    {/if}
                  </td>
                  <td class="td-hint">{run.source}</td>
                  <td class="td-action">
                    {#if run.agentId}
                      <button class="btn-secondary xs" on:click={() => watchAgent(run.agentId, run.sessionId)}>Open Activity</button>
                    {:else}
                      <button class="btn-secondary xs" on:click={() => location.hash = 'activity'}>Open Activity</button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {/if}
  </section>

  <!-- Cron agents (manageable) -->
  <section class="section">
    <div class="section-hdr">
      <span>Scheduled agents</span>
      <span class="pill">{cronAgents.length}</span>
    </div>
    {#if cronAgents.length === 0}
      <div class="empty">No agents with <code>trigger: cron</code>. Create one in Studio or add a SOUL.yaml.</div>
    {:else}
      <table class="tbl">
        <thead>
          <tr><th>ID</th><th>Name</th><th>Cron</th><th>Output bot</th><th>Enabled</th><th>Status</th><th class="td-action">Actions</th></tr>
        </thead>
        <tbody>
          {#each cronAgents as a}
            {@const st = statusById[a.id] || {}}
            {@const out = outputSummary(a)}
            <tr class:row-running={st.running}>
              <td class="td-mono">{a.id}</td>
              <td>{a.name}</td>
              <td class="td-mono">{a.schedule?.cron || '—'}</td>
              <td>
                {#if out}
                  <div class="output-cell">
                    <strong>{out.name}</strong>
                    <span>{out.channel}{out.to ? ` → ${out.to}` : ''}</span>
                  </div>
                {:else}
                  <button class="delivery-warn" on:click={() => openEdit(a)}
                    title="This cron agent has no delivery target — its results only go to the logs. Click to set an output channel.">
                    ⚠ No delivery
                  </button>
                {/if}
              </td>
              <td><span class="pill" class:pill-ok={a.enabled}>{a.enabled ? 'Yes' : 'No'}</span></td>
              <td>
                {#if st.running}
                  <span class="status running"><span class="dot"></span> Running…</span>
                {:else if st.imminent}
                  <span class="status soon">⏰ runs in {fmtCountdown(st.ms)}</span>
                {:else if st.hasNext}
                  <span class="status idle">next {fmtCountdown(st.ms)}</span>
                {:else}
                  <span class="status idle">—</span>
                {/if}
              </td>
              <td class="td-action">
                <div class="actions">
                  {#if st.running}
                    <button class="btn-secondary xs is-running" disabled><span class="dot"></span> Running</button>
                  {:else}
                    <button class="btn-primary xs" on:click={() => onRunClick(a)} disabled={busy[a.id]} title="Run now">
                      {busy[a.id] ? '…' : '▶ Run'}
                    </button>
                  {/if}
                  <button class="btn-secondary xs" on:click={() => openHistory(a)} title="Run history">📋 History</button>
                  <button class="btn-secondary xs" on:click={() => watchAgent(a.id)} title="Watch action log">👁 Watch</button>
                  {#if out}
                    <button class="btn-secondary xs" on:click={() => testOutput(a)} disabled={busy[a.id]} title="Send a test message to the configured output destination">Test output</button>
                  {/if}
                  <button class="btn-secondary xs" on:click={() => openEdit(a)} disabled={busy[a.id]}>Edit</button>
                  <button class="btn-secondary xs" on:click={() => clone(a.id)} disabled={busy[a.id]}>Clone</button>
                  <button class="btn-danger xs" on:click={() => remove(a.id)} disabled={busy[a.id] || st.running}>Delete</button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <div class="info-card">
    <h3>Setting up a scheduled agent</h3>
    <p>Set <code>trigger: cron</code> and a schedule block in the agent's SOUL.yaml:</p>
    <pre class="code-block">trigger: cron
schedule:
  cron: "0 8 * * *"   # every day at 8 AM
  output:
    channel: "telegram-financial-agent"
    to: "123456789"
    bot_name: "Finance Bot"</pre>
    <p><strong>▶ Run</strong> triggers a manual run immediately and returns the result here. Cron-fired runs use <code>schedule.output</code> to publish through the selected bot. While a job runs it shows <strong>Running</strong> and can't be started again (a scheduled fire is also skipped).</p>
  </div>
</div>

<!-- Edit modal -->
{#if editing}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close schedule edit modal"
    on:click|self={closeEdit}
    on:keydown={(e) => e.key === 'Escape' && closeEdit()}
  >
    <div class="modal">
      <h2>Edit schedule — {editing.id}</h2>
      <label class="field">
        <span class="field-label">Cron expression</span>
        <input type="text" bind:value={editCron} placeholder="0 8 * * *" />
        <span class="field-help">5-field cron (min hour day month weekday). Example: <code>0 7 * * *</code> = 7 AM daily.</span>
      </label>
      <div class="output-editor">
        <div class="output-editor-head">
          <span>Missed runs</span>
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={editRunMissedOnStartup} />
          <span>Run the latest missed cron after startup</span>
        </label>
        {#if editRunMissedOnStartup}
          <label class="field">
            <span class="field-label">Catch-up window</span>
            <input type="text" bind:value={editMissedStartupWindow} placeholder="24h" />
            <span class="field-help">Examples: <code>6h</code>, <code>24h</code>, <code>72h</code>.</span>
          </label>
        {/if}
      </div>
      <div class="output-editor">
        <div class="output-editor-head">
          <span>Scheduled output</span>
          {#if outputBotOptions.length === 0}<em>No channel bots configured</em>{/if}
        </div>
        <div class="field-help output-help">
          Add or rotate Telegram output bot tokens in <a href="#channels">Delivery</a>, then restart the gateway and select the bot here.
        </div>
        <label class="field">
          <span class="field-label">Bot</span>
          <select value={editOutputChannel} on:change={(e) => selectOutputChannel(e.currentTarget.value)} disabled={outputBotOptions.length === 0}>
            <option value="">Do not send output</option>
            {#each outputBotOptions as opt}
              <option value={opt.channel}>{opt.label}{opt.connected ? '' : ' · offline'}</option>
            {/each}
          </select>
          <span class="field-help">The scheduler sends the agent reply through this channel adapter ID.</span>
        </label>
        {#if editOutputChannel}
          <label class="field">
            <span class="field-label">Destination ID</span>
            <input type="text" bind:value={editOutputTo} placeholder="Telegram chat ID, Slack channel ID, Discord channel ID, or WhatsApp number" />
            <span class="field-help">This becomes the outbound message thread/chat/channel/user ID for the selected bot.</span>
          </label>
          <label class="field">
            <span class="field-label">Bot name</span>
            <input type="text" bind:value={editOutputBotName} placeholder="Friendly bot name" />
          </label>
          <label class="field">
            <span class="field-label">Template</span>
            <textarea bind:value={editOutputTemplate} rows="3" placeholder="&#123;reply&#125;"></textarea>
            <span class="field-help">Optional. Tokens: <code>&#123;reply&#125;</code>, <code>&#123;agent_id&#125;</code>, <code>&#123;agent_name&#125;</code>, <code>&#123;trigger&#125;</code>, <code>&#123;timestamp&#125;</code>.</span>
          </label>
        {/if}
      </div>
      <label class="check">
        <input type="checkbox" bind:checked={editEnabled} />
        <span>Enabled (registers the schedule with the scheduler)</span>
      </label>
      <div class="modal-row">
        <button class="btn-secondary" on:click={closeEdit} disabled={saving}>Cancel</button>
        <button class="btn-primary" on:click={saveEdit} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- History slide-out panel -->
{#if historyAgent}
  <div
    class="panel-bg"
    role="button"
    tabindex="0"
    aria-label="Close run history panel"
    on:click|self={closeHistory}
    on:keydown={(e) => e.key === 'Escape' && closeHistory()}
  >
    <div class="history-panel">
      <div class="panel-hdr">
        <div class="panel-title">
          <span class="panel-label">Run history</span>
          <span class="panel-agent">{historyAgent.name || historyAgent.id}</span>
        </div>
        <div class="panel-actions">
          <button class="btn-secondary xs" on:click={() => watchAgent(historyAgent.id)}>Open Activity</button>
          <button class="close-btn" on:click={closeHistory}>✕</button>
        </div>
      </div>

      <div class="panel-body">
        {#if historyLoading}
          <div class="panel-empty">Loading…</div>
        {:else if historyError}
          <div class="panel-empty err">{historyError}</div>
        {:else if historyRuns.length === 0}
          <div class="panel-empty">No runs recorded yet.</div>
        {:else}
          {#if historySourceSummary}
            <div class="history-note">{historySourceSummary}</div>
          {/if}
          {#each historyRuns as run (run.id)}
            <div class="run" class:run-fail={run.status === 'failed'} class:run-degraded={run.status === 'degraded'}>
              <button class="run-hdr" on:click={() => toggleRun(run.id)}>
                <span class="run-badge" class:badge-ok={run.status === 'success'} class:badge-fail={run.status === 'failed'} class:badge-warn={run.status === 'degraded'} class:badge-unk={run.status === 'unknown'}>
                  {run.status === 'success' ? '✓ success' : run.status === 'failed' ? '✗ failed' : run.status === 'degraded' ? '⚠ degraded' : '? unknown'}
                </span>
                <span class="run-time">{run.startTime ? new Date(run.startTime).toLocaleString() : '—'}</span>
                {#if run.channel}<span class="run-channel">{run.channel}</span>{/if}
                {#if run.source}<span class="run-channel">{run.source}</span>{/if}
                {#if run.steps}<span class="run-channel">{run.steps} step{run.steps === 1 ? '' : 's'}</span>{/if}
                {#if run.deliveryStatus}
                  <span class="run-channel" class:delivery-fail={run.deliveryStatus === 'failed'}>
                    {run.deliveryStatus === 'delivered' ? 'delivered' : 'delivery failed'}
                    {#if run.deliveryChannel} · {run.deliveryChannel}{/if}
                  </span>
                {/if}
                <span class="run-chevron">{expandedRuns[run.id] ? '▲' : '▼'}</span>
              </button>
              {#if expandedRuns[run.id]}
                <div class="run-output">
                  <div class="run-metrics-row">
                    <RunMetrics sessionId={run.sessionId} agentId={historyAgent.id} />
                  </div>
                  <button class="activity-link" on:click={() => watchAgent(historyAgent.id, run.sessionId)}>
                    Open this run in Activity
                  </button>
                  {#if run.deliveryStatus}
                    <div class="delivery-line" class:delivery-fail={run.deliveryStatus === 'failed'}>
                      Delivery: {run.deliveryStatus}
                      {#if run.deliveryChannel} via {run.deliveryChannel}{/if}
                      {#if run.deliveryTo} to {run.deliveryTo}{/if}
                      {#if run.deliveryError} — {run.deliveryError}{/if}
                    </div>
                  {/if}
                  {#if run.output}
                    <pre>{run.output}</pre>
                  {:else}
                    <span class="no-output">No output captured.</span>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    </div>
  </div>
{/if}

<!-- Run-now / wait prompt (on click when imminent, or auto when a run approaches) -->
{#if runPrompt}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Wait for scheduled run"
    on:click|self={promptWait}
    on:keydown={(e) => e.key === 'Escape' && promptWait()}
  >
    <div class="modal">
      <h2>⏰ {runPrompt.agent.name || runPrompt.agent.id} is about to run</h2>
      <p>
        This job is scheduled to run at <strong>{fmtTime(runPrompt.next)}</strong>
        — <strong>{fmtCountdown(new Date(runPrompt.next).getTime() - now)}</strong> from now.
        {#if runPrompt.auto}Heads up: it will start on its own at that time.{/if}
      </p>
      <p class="sub">Do you want to run it now, or wait for the scheduled time?</p>
      <div class="modal-row">
        <button class="btn-secondary" on:click={promptWait}>Wait for schedule</button>
        <button class="btn-primary" on:click={promptRunNow}>Run now</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .empty  { padding: 2rem 1.25rem; color: #6b7294; font-size: .85rem; }
  .empty code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  .section     { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .section-hdr {
    display: flex; align-items: center; gap: .7rem;
    padding: .8rem 1.25rem; border-bottom: 1px solid #1a1e36;
    font-size: .875rem; font-weight: 600;
  }
  .section-toggle {
    display: inline-flex; align-items: center; gap: .55rem;
    min-width: 0; padding: .2rem .25rem .2rem 0;
    background: none; border: none; color: inherit; font: inherit;
    cursor: pointer; text-align: left;
  }
  .section-toggle:hover { color: #e0e1f0; }
  .section-toggle:focus-visible {
    outline: 2px solid #6c63ff; outline-offset: 3px; border-radius: 4px;
  }
  .section-chevron {
    width: .75rem; color: #7b82a8; font-size: .8rem; text-align: center;
  }
  .hdr-action { margin-left: auto; }
  .history-depth {
    margin-left: auto; display: inline-flex; align-items: center; gap: .35rem;
    color: #6b7294; font-size: .75rem; font-weight: 500;
  }
  .history-depth select {
    background: #1c1f35; border: 1px solid #2a2f4a; color: #c8cadf;
    border-radius: 6px; padding: .22rem .45rem; font-size: .75rem;
  }
  .history-depth + .hdr-action { margin-left: 0; }
  .pill    { font-size: .7rem; padding: .15rem .5rem; border-radius: 999px; background: #1c1f35; color: #6b7294; }
  .pill-ok { background: rgba(76,175,130,.15); color: #4caf82; }

  .tbl { width: 100%; border-collapse: collapse; font-size: .85rem; }

  @media (max-width: 768px) {
    /* Wide tables scroll horizontally instead of overflowing the viewport */
    .tbl { display: block; overflow-x: auto; }
    .tbl th, .tbl td { padding: .6rem .75rem; }
  }
  .tbl th { padding: .6rem 1.25rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #1a1e36; }
  .tbl td { padding: .7rem 1.25rem; border-bottom: 1px solid #0e1020; vertical-align: middle; }
  .tbl tr:last-child td { border-bottom: none; }
  .tbl tr:hover td { background: rgba(255,255,255,.02); }
  .row-running td { background: rgba(76,175,130,.06); }
  .row-failed td { background: rgba(240,96,96,.035); }
  .row-degraded td { background: rgba(245,167,66,.035); }

  .td-name   { font-weight: 500; }
  .td-mono   { font-family: monospace; font-size: .8rem; color: #8b85ff; }
  .td-hint   { color: #555a7a; font-size: .78rem; }
  .catchup        { white-space: nowrap; cursor: help; }
  .catchup.on     { color: #6fbf8f; }
  /* E4b — startup-catch-up chip on the Automations row */
  .backfill-chip {
    display: inline-block;
    margin-left: .4rem;
    padding: .05rem .35rem;
    font-size: .68rem;
    background: #2a1e12;
    color: #f0a060;
    border: 1px solid #4a331d;
    border-radius: 4px;
    cursor: help;
    vertical-align: middle;
  }
  .missed-help    { margin-top: .5rem; max-width: 70ch; }
  .td-action { text-align: right; }
  .actions   { display: flex; gap: .35rem; justify-content: flex-end; flex-wrap: wrap; }
  .xs { padding: .28rem .6rem; font-size: .75rem; border-radius: 6px; }
  .output-cell { display: flex; flex-direction: column; gap: .15rem; min-width: 150px; }
  .output-cell strong { color: #c8cadf; font-size: .78rem; font-weight: 600; }
  .output-cell span { color: #6b7294; font-family: monospace; font-size: .7rem; word-break: break-all; }
  .delivery-warn { background: rgba(240,160,96,.12); border: 1px solid rgba(240,160,96,.4); color: #f0b070; border-radius: 7px; padding: .18rem .5rem; font-size: .72rem; font-weight: 600; cursor: pointer; white-space: nowrap; }
  .delivery-warn:hover { background: rgba(240,160,96,.2); }

  /* status cell */
  .status { font-size: .78rem; display: inline-flex; align-items: center; gap: .35rem; }
  .status.idle    { color: #555a7a; }
  .status.soon    { color: #f0a060; font-weight: 600; }
  .status.running { color: #4caf82; font-weight: 600; }
  .dot {
    width: .55rem; height: .55rem; border-radius: 50%;
    background: #4caf82; display: inline-block;
    box-shadow: 0 0 0 0 rgba(76,175,130,.6); animation: pulse 1.3s infinite;
  }
  .is-running { color: #4caf82 !important; }
  .is-running .dot { width: .5rem; height: .5rem; }
  @keyframes pulse {
    0%   { box-shadow: 0 0 0 0 rgba(76,175,130,.5); }
    70%  { box-shadow: 0 0 0 .35rem rgba(76,175,130,0); }
    100% { box-shadow: 0 0 0 0 rgba(76,175,130,0); }
  }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .6rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .85rem 1rem; font-family: monospace; font-size: .8rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre;
  }

  /* History panel */
  .panel-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.45); z-index: 100;
    display: flex; justify-content: flex-end;
  }
  .history-panel {
    width: min(520px, 100vw); max-width: 100vw; height: 100%;
    background: #141626; border-left: 1px solid #2a2f4a;
    display: flex; flex-direction: column; overflow: hidden;
  }
  .panel-hdr {
    display: flex; align-items: center; justify-content: space-between;
    padding: 1rem 1.25rem; border-bottom: 1px solid #1a1e36; flex-shrink: 0;
  }
  .panel-title { display: flex; flex-direction: column; gap: .2rem; }
  .panel-actions { display: flex; align-items: center; gap: .5rem; }
  .panel-label { font-size: .68rem; text-transform: uppercase; letter-spacing: .07em; color: #555a7a; font-weight: 600; }
  .panel-agent { font-size: .95rem; font-weight: 600; color: #e0e1f0; }
  .close-btn {
    background: none; border: none; color: #555a7a; font-size: 1rem;
    cursor: pointer; padding: .25rem .5rem; border-radius: 6px;
  }
  .close-btn:hover { background: #1c1f35; color: #c8cadf; }
  .panel-body { flex: 1; overflow-y: auto; display: flex; flex-direction: column; }
  .panel-empty { padding: 3rem 1.5rem; color: #555a7a; font-size: .85rem; text-align: center; }
  .panel-empty.err { color: #f06060; }
  .history-note {
    margin: .8rem 1.25rem .35rem; padding: .45rem .6rem; border-radius: 7px;
    background: rgba(108,99,255,.08); border: 1px solid rgba(108,99,255,.22);
    color: #aeb3dc; font-size: .72rem; line-height: 1.35;
  }

  .run { border-bottom: 1px solid #1a1e36; }
  .run:last-child { border-bottom: none; }
  .run-hdr {
    width: 100%; background: none; border: none; cursor: pointer;
    display: flex; align-items: center; gap: .6rem; flex-wrap: wrap;
    padding: .75rem 1.25rem; text-align: left;
  }
  .run-hdr:hover { background: rgba(255,255,255,.03); }
  .run-fail .run-hdr { background: rgba(240,96,96,.04); }
  .run-degraded .run-hdr { background: rgba(245,167,66,.04); }
  .run-badge {
    font-size: .7rem; font-weight: 600; padding: .15rem .55rem;
    border-radius: 999px; flex-shrink: 0;
  }
  .run-badge.inline { display: inline-flex; align-items: center; }
  .delivery-mini {
    display: inline-flex;
    align-items: center;
    padding: .12rem .45rem;
    border-radius: 999px;
    background: rgba(76,175,130,.12);
    color: #4caf82;
    font-family: monospace;
    font-size: .7rem;
    white-space: nowrap;
  }
  .delivery-mini.delivery-fail {
    background: rgba(240,96,96,.12);
    color: #ff9292;
  }
  .badge-ok  { background: rgba(76,175,130,.15); color: #4caf82; }
  .badge-fail{ background: rgba(240,96,96,.15);  color: #f06060; }
  .badge-warn{ background: rgba(245,167,66,.15); color: #f5bd67; }
  .badge-unk { background: rgba(100,100,120,.15); color: #888; }
  .run-time    { font-size: .78rem; color: #7b82a8; flex: 1; }
  .run-channel { font-size: .7rem; color: #555a7a; font-family: monospace; }
  .run-channel.delivery-fail, .delivery-line.delivery-fail { color: #ff9292; }
  .run-chevron { font-size: .65rem; color: #555a7a; margin-left: auto; }
  .run-output {
    padding: .75rem 1.25rem 1rem; background: #0e1020;
    border-top: 1px solid #1a1e36;
  }
  .delivery-line {
    color: #9aa2d8;
    font-size: .76rem;
    margin-bottom: .5rem;
  }
  .activity-link {
    margin: 0 0 .55rem;
    background: rgba(108,99,255,.12);
    border: 1px solid rgba(108,99,255,.35);
    color: #ada8ff;
    border-radius: 6px;
    padding: .25rem .55rem;
    font-size: .72rem;
    cursor: pointer;
  }
  .activity-link:hover { background: rgba(108,99,255,.2); }
  .run-output pre {
    font-family: monospace; font-size: .78rem; color: #b0b5d8;
    line-height: 1.65; white-space: pre-wrap; word-break: break-word; margin: 0;
  }
  .no-output { font-size: .78rem; color: #555a7a; font-style: italic; }

  /* Modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,.65); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: #141626; border: 1px solid #2a2f4a; border-radius: 12px; padding: 1.5rem; width: 460px; max-width: 92vw; max-height: 88vh; overflow-y: auto; display: flex; flex-direction: column; gap: 1rem; }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { font-size: .85rem; color: #b0b5d8; line-height: 1.6; }
  .modal p.sub { color: #7b82a8; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .field select,
  .field textarea {
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 6px; padding: .5rem .65rem; font-size: .85rem;
  }
  .field textarea { resize: vertical; min-height: 4.5rem; font-family: inherit; line-height: 1.45; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .field-help code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }
  .output-editor {
    border: 1px solid #1a1e36; border-radius: 8px; background: #101323;
    padding: .85rem; display: flex; flex-direction: column; gap: .8rem;
  }
  .output-editor-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .output-editor-head span { font-size: .82rem; color: #e0e1f0; font-weight: 600; }
  .output-editor-head em { font-size: .72rem; color: #f0a060; font-style: normal; }
  .check { display: flex; align-items: center; gap: .5rem; font-size: .82rem; color: #c8cadf; }
  .check input { width: auto; }
  .modal-row {
    display: flex; gap: .75rem; justify-content: flex-end;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }

  .run-metrics-row { padding: .2rem 0 .45rem; }
</style>
