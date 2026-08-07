<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { connected } from '../lib/stores.js'
  import { api, createEventSocket } from '../lib/api.js'
  import { waitForGateway, waitingMessage, timeoutMessage, UPGRADE_BUDGET } from '../lib/gatewaywait.js'

  let status  = null
  let agents  = []
  let events  = []
  let ws      = null
  let error   = null
  let authError = false
  let eventsEl
  let eventFilter = 'all'
  let suggestions = []
  let dismissed = new Set()
  let readiness = null
  let ops = null
  let opsError = ''
  let alertTestStatus = ''
  let downloadingSupport = false
  let supportMessage = ''
  // F-GUI-4 — Cohort F S4 detail: the /readiness journey already lists the
  // security row, but the operator needs the concrete blocker/warning text and
  // a one-click deep-link into the affected agent's Security Doctor. That
  // requires the richer /security/readiness payload.
  let securityReadiness = null

  let updateInfo = null
  let upgrading = false
  let upgradeMessage = ''
  let upgradeError = ''

  const EVENT_FILTERS = [
    { id: 'all', label: 'All' },
    { id: 'errors', label: 'Errors' },
    { id: 'tools', label: 'Tools' },
    { id: 'llm', label: 'LLM' },
    { id: 'messages', label: 'Messages' },
  ]

  async function load() {
    // /health is unauthenticated — it tells us whether the gateway is up,
    // independent of whether our credentials are valid.
    try {
      status = await api.health()
    } catch {
      status = null
    }
    try {
      const res = await api.agents.list()
      agents    = res.agents || []
      error     = null
      authError = false
    } catch (e) {
      error     = e.message
      authError = e.status === 401 || e.status === 403
    }
    try {
      const res = await api.proactive.suggestions()
      suggestions = (res.suggestions || []).filter(s => !dismissed.has(s.kind + ':' + s.agent_id))
    } catch {
      suggestions = []
    }
    try {
      readiness = await api.readiness()
    } catch {
      readiness = null
    }
    try {
      securityReadiness = await api.security.readiness()
    } catch {
      securityReadiness = null
    }
    try {
      ops = await api.opsSummary('24h')
      opsError = ''
    } catch (e) {
      ops = null
      opsError = e.status === 503 ? 'Run reliability needs durable action logging.' : (e.message || 'Run reliability unavailable.')
    }
    try {
      updateInfo = await api.updates.status()
    } catch {
      updateInfo = null
    }
  }

  async function startUpgrade() {
    if (!confirm(`Are you sure you want to upgrade to ${updateInfo.latest_version}? The gateway will restart automatically.`)) {
      return
    }
    upgrading = true
    upgradeError = ''
    upgradeMessage = 'Downloading and installing the update...'
    try {
      const res = await api.updates.upgrade()
      upgradeMessage = res.message || 'Upgrade installed. Waiting for the gateway to restart...'
      // Do NOT reload on a timer. The old process exits 250ms after this reply
      // and the replacement still has to boot and bind the port; a fixed 5s
      // wait reloaded into a dead port and showed "Failed to fetch" directly
      // under a message saying the upgrade had succeeded.
      const outcome = await waitForGateway(api.health, {
        ...UPGRADE_BUDGET,
        onAttempt: (n, total) => { upgradeMessage = waitingMessage(n, total) },
      })
      if (outcome.ok) { window.location.reload(); return }
      upgrading = false
      upgradeError = timeoutMessage('upgrade', outcome.waitedMs)
    } catch (e) {
      upgrading = false
      upgradeError = 'Upgrade failed: ' + (e.message || e)
    }
  }

  function dismiss(s) {
    dismissed.add(s.kind + ':' + s.agent_id)
    suggestions = suggestions.filter(x => x !== s)
  }


  function connectWS() {
    try { ws = createEventSocket() } catch { return }
    ws.onopen    = () => { $connected = true }
    ws.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data)
        events = [{ ...ev, _ts: Date.now() }, ...events].slice(0, 300)
      } catch {}
    }
    ws.onclose = () => {
      $connected = false
      setTimeout(connectWS, 3000)
    }
    ws.onerror = () => ws.close()
  }

  onMount(() => {
    load()
    connectWS()
    const t = setInterval(load, 15_000)
    return () => clearInterval(t)
  })

  onDestroy(() => { if (ws) ws.close() })

  function eventColor(type = '') {
    if (type.includes('error'))                        return '#f06060'
    if (type.includes('complete') || type.includes('reply')) return '#4caf82'
    if (type.includes('trigger') || type.includes('start'))  return '#6c63ff'
    if (type.includes('tool'))                         return '#f0a060'
    return '#6b7294'
  }

  function fmtTime(iso) {
    try { return new Date(iso || Date.now()).toLocaleTimeString() } catch { return '' }
  }

  function fmtData(data) {
    if (!data) return ''
    const s = typeof data === 'string' ? data : JSON.stringify(data)
    return s.slice(0, 140)
  }

  function matchesFilter(ev) {
    const type = ev.type || ''
    if (eventFilter === 'all') return true
    if (eventFilter === 'errors') return type.includes('error') || fmtData(ev.payload).toLowerCase().includes('error')
    if (eventFilter === 'tools') return type.includes('tool')
    if (eventFilter === 'llm') return type.includes('llm')
    if (eventFilter === 'messages') return type.includes('message')
    return true
  }

  function openHref(href) {
    if (!href) return
    window.location.hash = href.replace(/^#/, '')
  }

  async function sendOpsAlertTest() {
    alertTestStatus = 'Sending test alert...'
    try {
      const res = await api.opsAlertTest()
      alertTestStatus = `Sent to ${res.channel || 'channel'} ${res.to || ''}`.trim()
    } catch (e) {
      alertTestStatus = e.message || 'Alert test failed.'
    }
  }

  async function downloadSupportBundle() {
    downloadingSupport = true
    supportMessage = ''
    try {
      const { blob, filename } = await api.support.bundle()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename || `soulacy-support-${new Date().toISOString().slice(0, 19).replaceAll(':', '')}.zip`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      supportMessage = 'Support bundle includes readiness, doctor output, run ledger, redacted config, agent manifests, and recent logs.'
    } catch (e) {
      supportMessage = e.message || 'Could not prepare support bundle.'
    } finally {
      downloadingSupport = false
    }
  }

  function statusLabel(status) {
    if (status === 'ok') return 'Ready'
    if (status === 'warn') return 'Needs attention'
    return 'Blocked'
  }

  // F-GUI-4 — surface the highest-priority security next-action inline on the
  // journey row and deep-link the "Security defaults" card into the Security
  // Doctor drawer for the affected agent. Priority order:
  //   1. First privileged exposure that hasn't been accepted → hardest fix,
  //      routes to Agents#doctor.
  //   2. First wildcard-MCP agent → routes to Agents#doctor.
  //   3. First next-action → falls back to whatever the backend suggests.
  function securityHighlight() {
    const r = securityReadiness
    if (!r) return null
    const exposures = r.privileged_exposures || []
    const unaccepted = exposures.filter(e => !e.accepted)
    if (unaccepted.length > 0) {
      const first = unaccepted[0]
      return {
        text: `${first.agent_name || first.agent_id} exposes ${first.channels?.join(', ') || 'shared channels'} without ack`,
        agentId: first.agent_id,
      }
    }
    if (exposures.length > 0) {
      const first = exposures[0]
      return {
        text: `${first.agent_name || first.agent_id} privileged channels: ${first.channels?.join(', ') || 'shared channels'}`,
        agentId: first.agent_id,
      }
    }
    const wildcards = r.wildcard_mcp_agents || []
    if (wildcards.length > 0) {
      return { text: `wildcard MCP allow-list on ${wildcards[0]}`, agentId: wildcards[0] }
    }
    if (r.next_actions && r.next_actions.length > 0) {
      return { text: r.next_actions[0], agentId: '' }
    }
    return null
  }

  function openSecurityDoctor(agentId) {
    if (!agentId) {
      window.location.hash = 'agents'
      return
    }
    const params = new URLSearchParams({ agent_id: agentId, doctor: '1' })
    window.location.hash = `agents?${params.toString()}`
  }

  function pct(n) {
    if (!Number.isFinite(Number(n))) return '0%'
    return `${Math.round(Number(n) * 100)}%`
  }

  function fmtUSD(n) {
    n = Number(n || 0)
    if (n === 0) return '$0.00'
    if (n < 0.01) return `$${n.toFixed(4)}`
    return `$${n.toFixed(2)}`
  }

  function fmtMs(ms) {
    ms = Number(ms || 0)
    if (!ms) return 'n/a'
    if (ms < 1000) return `${ms}ms`
    const sec = ms / 1000
    if (sec < 60) return `${sec.toFixed(sec < 10 ? 1 : 0)}s`
    return `${(sec / 60).toFixed(1)}m`
  }

  function shortError(s) {
    s = String(s || '').trim()
    return s.length > 110 ? s.slice(0, 110) + '…' : s
  }

  $: filteredEvents = events.filter(matchesFilter)
</script>

<div class="page">
  <div class="page-header">
    <h1>Dashboard</h1>
    <button class="btn-secondary" on:click={load}>↺ Refresh</button>
        <TourButton />
    </div>

  {#if error}
    <div class="banner err">
      {#if authError}
        🔒 Authentication required — click 🔑 in the sidebar to set your API key
      {:else}
        ⚠ {error}
      {/if}
    </div>
  {/if}

  {#if updateInfo && updateInfo.update_available}
    <div class="update-banner">
      <div class="update-banner-icon">✨</div>
      <div class="update-banner-content">
        <strong>Update Available</strong>
        <span>Soulacy {updateInfo.latest_version} is available! (Current: {updateInfo.current_version}).</span>
      </div>
      <div class="update-banner-actions">
        <button class="btn-primary btn-sm" on:click={startUpgrade}>Upgrade Now</button>
        <button class="btn-secondary btn-sm" on:click={() => window.open("https://github.com/vmodekurti/soulacy/releases/latest", "_blank")}>View Release Notes</button>
      </div>
    </div>
  {/if}

  {#if upgrading}
    <div class="upgrading-overlay">
      <div class="upgrading-spinner"></div>
      <div class="upgrading-text">Upgrading Soulacy...</div>
      <div class="upgrading-subtext">{upgradeMessage}</div>
    </div>
  {/if}

  {#if upgradeError}
    <div class="banner err upgrade-err">
      <span>{upgradeError}</span>
      <button class="linkish" on:click={() => window.location.reload()}>Reload</button>
    </div>
  {/if}

  <!-- Status cards -->
  <div class="cards">
    <div class="card" class:card-ok={!!status} data-tooltip="Active connection state of the local Soulacy gateway daemon">
      <div class="card-label">Gateway</div>
      <div class="card-value">{status ? '● Online' : authError ? '🔒 Authentication required' : '○ Offline'}</div>
      {#if status}<div class="card-sub">v{status.version}</div>{/if}
    </div>

    <div class="card" data-tooltip="Total number of active multi-agent runtimes loaded in your workspace">
      <div class="card-label">Deployed Agents</div>
      <div class="card-value">{agents.length}</div>
      <div class="card-sub">{agents.filter(a => a.enabled).length} enabled</div>
    </div>

    <div class="card" data-tooltip="Completed agent execution turns and workflow sessions since dashboard launch">
      <div class="card-label">Runs (session)</div>
      <div class="card-value">{events.length}</div>
      <div class="card-sub">{filteredEvents.length} shown · {$connected ? 'streaming live' : 'reconnecting…'}</div>
    </div>
  </div>

  {#if ops || opsError}
    <div class="ops">
      <div class="ops-top">
        <div>
          <div class="eyebrow">Run Reliability</div>
          <h2>
            {#if ops}
              {ops.total_runs || 0} runs · {pct(ops.failure_rate)} failure rate
            {:else}
              Not available
            {/if}
          </h2>
          <p>
            {#if ops}
              Last 24h · {ops.successful_runs || 0} successful · {ops.failed_runs || 0} failed · {ops.incomplete_runs || 0} incomplete
              {#if ops.total_tokens !== undefined} · {(ops.total_tokens || 0).toLocaleString()} tokens · {fmtUSD(ops.cost_usd)}{/if}
            {:else}
              {opsError}
            {/if}
          </p>
        </div>
        <button class="btn-secondary" on:click={() => openHref('#activity')}>Open Runs</button>
      </div>
      {#if ops}
        {#if readiness?.slo}
          <div class="slo-strip {readiness.slo.status}" data-tooltip="Evaluation of workspace reliability metrics against defined SLO thresholds">
            <div>
              <div class="ops-label">Production SLO</div>
              <strong>{readiness.slo.score || 0}% · {statusLabel(readiness.slo.status)}</strong>
              <span>
                {readiness.slo.summary?.total_runs || 0} runs in {readiness.slo.window || '24h'} ·
                fail {pct(readiness.slo.summary?.failure_rate || 0)} ·
                incomplete {pct(readiness.slo.summary?.incomplete_rate || 0)} ·
                P95 {fmtMs(readiness.slo.summary?.p95_duration_ms)}
              </span>
            </div>
            <button class="btn-secondary" on:click={() => openHref('#config')}>Tune SLOs</button>
          </div>
        {/if}
        {#if readiness?.ops_alerts}
          <div class="slo-strip {readiness.ops_alerts.status}" data-tooltip="Configuration state of active operations notification channels">
            <div>
              <div class="ops-label">Ops Alert Delivery</div>
              <strong>{statusLabel(readiness.ops_alerts.status)} · {readiness.ops_alerts.channel || 'not configured'}</strong>
              <span>
                {readiness.ops_alerts.to ? `destination ${readiness.ops_alerts.to}` : 'No destination configured'} ·
                alerts fire at {readiness.ops_alerts.min_status || 'fail'}
              </span>
              {#if alertTestStatus}<span>{alertTestStatus}</span>{/if}
            </div>
            <div class="inline-actions">
              <button class="btn-secondary" on:click={() => openHref('#config')}>Configure</button>
              <button class="btn-secondary" on:click={sendOpsAlertTest}>Send test</button>
            </div>
          </div>
        {/if}
        <div class="ops-grid">
          <div class="ops-panel">
            <div class="ops-label">Top Failing Agents</div>
            {#if ops.top_failing_agents?.length}
              {#each ops.top_failing_agents as row}
                <button class="ops-row" type="button" on:click={() => openHref(`#activity?agent_id=${encodeURIComponent(row.agent_id)}`)}>
                  <strong>{row.agent_id}</strong>
                  <span>{row.failures} failure{row.failures === 1 ? '' : 's'} · {pct(row.failure_rate)}</span>
                </button>
              {/each}
            {:else}
              <div class="ops-empty">No failing agents in this window.</div>
            {/if}
          </div>
          <div class="ops-panel">
            <div class="ops-label">Recent Failures</div>
            {#if ops.recent_failures?.length}
              {#each ops.recent_failures as fail}
                <button class="ops-row" type="button" on:click={() => openHref(`#activity?agent_id=${encodeURIComponent(fail.agent_id)}&session_id=${encodeURIComponent(fail.session_id)}`)}>
                  <strong>{fail.agent_id}</strong>
                  <span>{shortError(fail.error)}</span>
                </button>
              {/each}
            {:else}
              <div class="ops-empty">No recent run failures.</div>
            {/if}
          </div>
          <div class="ops-panel">
            <div class="ops-label">Error Signatures</div>
            {#if ops.top_errors?.length}
              {#each ops.top_errors as err}
                <div class="ops-row static">
                  <strong>{err.count}×</strong>
                  <span>{shortError(err.message)}</span>
                </div>
              {/each}
            {:else}
              <div class="ops-empty">No error signatures captured.</div>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}

  {#if readiness}
    <div class="readiness">
      <div class="readiness-top">
        <div>
          <div class="eyebrow">Launch Readiness</div>
          <h2>{readiness.summary?.score || 0}% · {readiness.summary?.status === 'ready' ? 'Ready' : readiness.summary?.status === 'at_risk' ? 'At risk' : 'Needs setup'}</h2>
          <p>
            {readiness.summary?.ready_items || 0}/{readiness.summary?.total_items || 0} checks ready ·
            {readiness.summary?.blocker_items || 0} blockers ·
            {readiness.summary?.warning_items || 0} warnings
          </p>
          <p>
            {readiness.summary?.enabled_agents || 0} enabled agents ·
            {readiness.summary?.providers_ready || 0} providers ·
            {readiness.summary?.channels_ready || 0} channels ·
            {readiness.summary?.learning_agents || 0} learning loops
          </p>
        </div>
        <div class="readiness-actions">
          <button class="btn-primary" on:click={() => openHref('#studio')}>Open Studio</button>
          <button class="btn-secondary" on:click={downloadSupportBundle} disabled={downloadingSupport} data-tooltip="Generate a ZIP archive of diagnostics, configs, and active logs for troubleshooting">
            {downloadingSupport ? 'Preparing...' : 'Download support bundle'}
          </button>
        </div>
      </div>
      {#if supportMessage}
        <div class:ok-note={!supportMessage.toLowerCase().includes('could not')} class:err-note={supportMessage.toLowerCase().includes('could not')}>
          {supportMessage}
        </div>
      {/if}

      {#if readiness.release}
        <div class:release-strip={true} class:ok={readiness.release?.updates_ready} class:warn={!readiness.release?.updates_ready}>
          <div>
            <div class="release-title">
              <span>{readiness.release?.updates_ready ? 'Updates configured' : 'Updates need setup'}</span>
              <small>{readiness.release?.version || 'unknown version'}</small>
            </div>
            <p>{readiness.release?.update_hint || 'Configure a release manifest before production launch.'}</p>
            {#if readiness.release?.update_manifest}
              <code>{readiness.release.update_manifest}</code>
            {/if}
          </div>
          <div class="release-cmds">
            <code>{readiness.release?.dry_run_command || 'sy update install --dry-run'}</code>
            <code>{readiness.release?.install_command || 'sy update install --yes'}</code>
          </div>
        </div>
      {/if}

      {#if readiness.deployment}
        <div class:release-strip={true}
             class:ok={readiness.deployment.status === 'ok'}
             class:warn={readiness.deployment.status === 'warn'}
             class:fail={readiness.deployment.status === 'fail'}>
          <div>
            <div class="release-title">
              <span>{readiness.deployment.label || 'Local'} deployment</span>
              <small>{readiness.deployment.strict ? 'strict launch gate' : 'advisory checks'}</small>
            </div>
            <p>
              {readiness.deployment.ready || 0}/{readiness.deployment.total || 0} deployment checks ready ·
              {statusLabel(readiness.deployment.status)}
            </p>
            {#if readiness.deployment.owner || readiness.deployment.region || readiness.deployment.notes}
              <code>
                {readiness.deployment.owner || 'unowned'}
                {#if readiness.deployment.region} · {readiness.deployment.region}{/if}
                {#if readiness.deployment.notes} · {readiness.deployment.notes}{/if}
              </code>
            {/if}
          </div>
          <button class="btn-secondary" on:click={() => openHref('#config')}>Edit profile</button>
        </div>
      {/if}

      {#if readiness.studio_contracts}
        <div class:release-strip={true}
             class:ok={readiness.studio_contracts.status === 'ok'}
             class:warn={readiness.studio_contracts.status === 'warn'}
             class:fail={readiness.studio_contracts.status === 'fail'}>
          <div>
            <div class="release-title">
              <span>Studio contract health</span>
              <small>{readiness.studio_contracts.score || 0}% · {statusLabel(readiness.studio_contracts.status)}</small>
            </div>
            <p>
              {readiness.studio_contracts.checked || 0} checked ·
              {readiness.studio_contracts.passing || 0} passing ·
              {readiness.studio_contracts.blockers || 0} blockers ·
              {readiness.studio_contracts.warnings || 0} warnings
            </p>
            {#if readiness.studio_contracts.worst_agent || readiness.studio_contracts.worst_summary}
              <code>
                {readiness.studio_contracts.worst_agent || 'Saved agents'}
                {#if readiness.studio_contracts.worst_summary} · {readiness.studio_contracts.worst_summary}{/if}
              </code>
            {/if}
          </div>
          <button class="btn-secondary" on:click={() => openHref('#studio')}>Open Studio</button>
        </div>
      {/if}

      {#if readiness.launch_checklist?.length}
        <div class="launch-checklist">
          <div class="section-hdr inline">
            <span>Production gate</span>
            <span class="pill">{readiness.launch_checklist.length}</span>
          </div>
          <div class="launch-list">
            {#each readiness.launch_checklist as item}
              <button class="launch-row {item.status}" type="button" on:click={() => openHref(item.href)}>
                <span class="journey-status">{statusLabel(item.status)}</span>
                <strong>{item.label}</strong>
                <small>{item.detail}</small>
                {#if item.remedy}
                  <em>{item.remedy}</em>
                {/if}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      <!-- The competitive-parity cockpit (readiness.parity) used to render here.
           It was removed because it's a product/roadmap artifact — comparing
           this workspace against reference frameworks doesn't help an operator
           decide what to fix today, and its "biggest parity gaps" overlapped
           with Next Best Actions below. The backend still computes
           readiness.parity for API consumers who want the scorecard. -->

      <div class="journey-grid">
        {#each readiness.journey || [] as item}
          {#if item.key === 'security'}
            <!-- F-GUI-4 — the S4 backend already emits a Security row in
                 readiness.journey. We enrich it here with the highest-priority
                 blocker/warning text and route clicks to the Security Doctor
                 for the affected agent when we can identify one. -->
            {@const hl = securityHighlight()}
            <button
              class="journey-card {item.status} security-card"
              type="button"
              on:click={() => hl?.agentId ? openSecurityDoctor(hl.agentId) : openHref(item.href)}
              title={hl?.text || item.detail}
            >
              <span class="journey-status">{statusLabel(item.status)}</span>
              <strong>{item.label}</strong>
              <small>{item.detail}</small>
              {#if hl}
                <span class="journey-inline">→ {hl.text}</span>
              {/if}
            </button>
          {:else}
            <button class="journey-card {item.status}" type="button" on:click={() => openHref(item.href)}>
              <span class="journey-status">{statusLabel(item.status)}</span>
              <strong>{item.label}</strong>
              <small>{item.detail}</small>
            </button>
          {/if}
        {/each}
      </div>

      {#if readiness.next_actions?.length}
        <div class="next-actions">
          <div class="section-hdr inline">
            <span>Next best actions</span>
            <span class="pill">{readiness.next_actions.length}</span>
          </div>
          {#each readiness.next_actions as action}
            <button class="action-row {action.status}" type="button" on:click={() => openHref(action.href)}>
              <span>{action.label}</span>
              <small>{action.detail}</small>
            </button>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Proactive suggestions -->
  {#if suggestions.length}
    <div class="section suggest-section">
      <div class="section-hdr">
        <span>Suggested for you</span>
        <span class="pill">{suggestions.length}</span>
      </div>
      <div class="suggest-list">
        {#each suggestions as s}
          <div class="suggest-card" class:review={s.kind === 'review'}>
            <div class="suggest-body">
              <div class="suggest-title">{s.title}</div>
              <div class="suggest-detail">{s.detail}</div>
              <div class="suggest-action">→ {s.action}</div>
            </div>
            <button class="suggest-dismiss" data-tooltip="Dismiss" on:click={() => dismiss(s)}>×</button>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- Live event log -->
  <div class="section">
    <div class="section-hdr">
      <span>Live Event Log</span>
      <span class="pill" class:pill-live={$connected}>{$connected ? '● Live' : '○ Reconnecting'}</span>
      <div class="filter-tabs" aria-label="Event filters">
        {#each EVENT_FILTERS as filter}
          <button
            class:active={eventFilter === filter.id}
            on:click={() => eventFilter = filter.id}
            type="button"
          >{filter.label}</button>
        {/each}
      </div>
      {#if events.length}
        <button class="btn-secondary" style="padding:0.2rem 0.6rem;font-size:0.72rem"
                on:click={() => events = []}>Clear</button>
      {/if}
    </div>

    <div class="log" bind:this={eventsEl}>
      {#if events.length === 0}
        <div class="empty">No events yet — send a chat or trigger an agent to see activity.</div>
      {:else if filteredEvents.length === 0}
        <div class="empty">No events match this filter.</div>
      {:else}
        {#each filteredEvents as ev (ev._ts + (ev.id || ''))}
          <div class="log-row">
            <span class="log-time">{fmtTime(ev.timestamp)}</span>
            <span class="log-type" style="color:{eventColor(ev.type)}">{ev.type || 'event'}</span>
            <span class="log-agent">{ev.agent_id || ''}</span>
            <span class="log-data">{fmtData(ev.payload)}</span>
          </div>
        {/each}
      {/if}
    </div>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }

  .banner { padding: 0.7rem 1rem; border-radius: 8px; font-size: 0.85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 1rem; }
  .card  {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px; padding: 1.1rem 1.25rem;
    transition: border-color 0.2s;
  }
  .card-ok    { border-color: rgba(76,175,130,.35); }
  .card-label { color: #6b7294; font-size: 0.72rem; text-transform: uppercase; letter-spacing: .06em; margin-bottom: .4rem; }
  .card-value { font-size: 1.45rem; font-weight: 600; }
  .card-sub   { color: #6b7294; font-size: 0.75rem; margin-top: .2rem; }

  .btn-primary {
    background: #6c63ff; color: white; border: 0; border-radius: 8px;
    padding: .55rem .9rem; font-weight: 650; cursor: pointer;
  }
  .btn-primary:hover { filter: brightness(1.08); }
  .btn-secondary {
    background: #20243d; color: #dfe2ff; border: 1px solid #30365f; border-radius: 8px;
    padding: .55rem .9rem; font-weight: 650; cursor: pointer;
  }
  .btn-secondary:hover:not(:disabled) { border-color: #6c63ff; }
  .btn-secondary:disabled { opacity: .65; cursor: not-allowed; }

  .readiness {
    background: #121525; border: 1px solid #24284a; border-radius: 10px;
    padding: 1rem; display: flex; flex-direction: column; gap: 1rem;
  }
  .ops {
    background: #121525; border: 1px solid #24284a; border-radius: 10px;
    padding: 1rem; display: flex; flex-direction: column; gap: .85rem;
  }
  .ops-top {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
  }
  .ops h2 { font-size: 1.05rem; margin: 0; }
  .ops p { margin: .35rem 0 0; color: #8a91b8; font-size: .78rem; }
  .ops-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: .65rem;
  }
  .slo-strip {
    display: flex; justify-content: space-between; align-items: center; gap: .75rem;
    border: 1px solid #252a4a; background: #101322; border-radius: 8px;
    padding: .7rem .8rem; margin-bottom: .75rem;
  }
  .slo-strip.ok { border-color: rgba(76,175,130,.35); }
  .slo-strip.warn { border-color: rgba(240,160,96,.42); }
  .slo-strip.fail { border-color: rgba(240,96,96,.45); }
  .slo-strip strong {
    display: block; color: #e8ebff; font-size: .86rem; margin-bottom: .2rem;
  }
  .slo-strip span { color: #8a91b8; font-size: .74rem; }
  .inline-actions {
    display: flex;
    align-items: center;
    gap: .45rem;
    flex-wrap: wrap;
  }
  .ops-panel {
    background: #0f1222; border: 1px solid #1a1e36; border-radius: 8px; overflow: hidden;
  }
  .ops-label {
    padding: .6rem .75rem; border-bottom: 1px solid #1a1e36;
    color: #7d84c9; font-size: .68rem; text-transform: uppercase;
    letter-spacing: .08em; font-weight: 700;
  }
  .ops-row {
    width: 100%; display: grid; grid-template-columns: minmax(92px, 130px) 1fr; gap: .65rem;
    text-align: left; background: transparent; border: 0; border-top: 1px solid #171b31;
    color: #dfe2ff; padding: .6rem .75rem; cursor: pointer;
  }
  .ops-row:first-of-type { border-top: 0; }
  .ops-row.static { cursor: default; }
  .ops-row:not(.static):hover { background: #171a2e; }
  .ops-row strong { font-size: .75rem; color: #f0f2ff; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ops-row span { font-size: .72rem; color: #8a91b8; line-height: 1.35; overflow-wrap: anywhere; }
  .ops-empty { padding: .8rem .75rem; color: #6b7294; font-size: .74rem; }
  .readiness-top {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
  }
  .eyebrow {
    color: #7d84c9; font-size: .68rem; text-transform: uppercase;
    letter-spacing: .08em; font-weight: 700; margin-bottom: .25rem;
  }
  .readiness h2 { font-size: 1.15rem; margin: 0; }
  .readiness p { margin: .35rem 0 0; color: #8a91b8; font-size: .78rem; }
  .readiness-actions { display: flex; gap: .5rem; flex-wrap: wrap; justify-content: flex-end; }
  .release-strip {
    display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem;
    border-radius: 8px; padding: .75rem .85rem; background: #0f1222;
    border: 1px solid #252a4a;
  }
  .release-strip.ok { border-color: rgba(76,175,130,.3); background: rgba(76,175,130,.06); }
  .release-strip.warn { border-color: rgba(240,160,96,.35); background: rgba(240,160,96,.07); }
  .release-strip.fail { border-color: rgba(240,96,96,.42); background: rgba(240,96,96,.07); }
  .release-title {
    display: flex; align-items: baseline; gap: .6rem; color: #dfe2ff;
    font-weight: 700; font-size: .82rem;
  }
  .release-title small { color: #7d84c9; font-size: .7rem; font-weight: 650; }
  .release-strip p { margin: .3rem 0 0; max-width: 760px; }
  .release-strip code {
    display: inline-block; margin-top: .35rem; background: #0a0d19;
    border: 1px solid #1a1e36; border-radius: 6px; padding: .18rem .4rem;
    color: #bfc5ff; font-size: .7rem; overflow-wrap: anywhere;
  }
  .release-cmds {
    display: flex; flex-direction: column; gap: .25rem; min-width: 230px; align-items: flex-end;
  }
  /* Parity cockpit styles removed with the block itself — the backend still
     serves readiness.parity, but the Dashboard no longer renders it. */
  .launch-checklist {
    background: #0f1222; border: 1px solid #1a1e36; border-radius: 8px; overflow: hidden;
  }
  .launch-list {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: .55rem;
    padding: .7rem;
  }
  .launch-row {
    text-align: left; background: #14182c; border: 1px solid #252a4a; border-radius: 8px;
    color: #dfe2ff; padding: .7rem; cursor: pointer; min-height: 126px;
    display: grid; grid-template-columns: auto 1fr; align-content: start; gap: .35rem .55rem;
  }
  .launch-row:hover { border-color: #6c63ff; }
  .launch-row.ok { border-color: rgba(76,175,130,.35); }
  .launch-row.warn { border-color: rgba(240,160,96,.42); }
  .launch-row.fail { border-color: rgba(240,96,96,.45); }
  .launch-row strong { font-size: .8rem; align-self: center; }
  .launch-row small {
    grid-column: 1 / -1; color: #8a91b8; font-size: .72rem; line-height: 1.35;
  }
  .launch-row em {
    grid-column: 1 / -1; color: #bfc5ff; font-style: normal; font-size: .7rem;
    line-height: 1.35; background: #0a0d19; border: 1px solid #1a1e36;
    border-radius: 6px; padding: .35rem .45rem;
  }
  .ok-note,
  .err-note {
    border-radius: 8px; padding: .55rem .75rem; font-size: .75rem;
  }
  .ok-note { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.25); color: #68d19b; }
  .err-note { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.25); color: #ff8585; }
  .journey-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: .65rem;
  }
  .journey-card {
    text-align: left; background: #171a2e; border: 1px solid #252a4a; border-radius: 8px;
    padding: .8rem; color: #dfe2ff; cursor: pointer; min-height: 116px;
  }
  .journey-card strong { display: block; font-size: .86rem; margin: .35rem 0; }
  .journey-card small { display: block; color: #8a91b8; font-size: .72rem; line-height: 1.35; }
  .journey-card.ok { border-color: rgba(76,175,130,.35); }
  .journey-card.warn { border-color: rgba(240,160,96,.45); }
  .journey-card.fail { border-color: rgba(240,96,96,.45); }
  .journey-card:hover { border-color: #6c63ff; }
  .journey-status {
    display: inline-flex; border-radius: 999px; padding: .15rem .45rem;
    font-size: .66rem; font-weight: 700; background: #202542; color: #b6bcf3;
  }
  .journey-card.ok .journey-status { background: rgba(76,175,130,.15); color: #68d19b; }
  .journey-card.warn .journey-status { background: rgba(240,160,96,.16); color: #f0b070; }
  .journey-card.fail .journey-status { background: rgba(240,96,96,.14); color: #ff8585; }
  /* F-GUI-4 — Cohort F: highlight the top security next-action inline on the
     Security row and reserve room for the additional line without breaking the
     rest of the grid. */
  .journey-card .journey-inline {
    display: block; color: #ada8ff; font-size: .7rem;
    margin-top: .35rem; line-height: 1.35;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .journey-card.security-card.fail .journey-inline { color: #ff8585; }
  .journey-card.security-card.warn .journey-inline { color: #f0b070; }
  .next-actions {
    background: #0f1222; border: 1px solid #1a1e36; border-radius: 8px; overflow: hidden;
  }
  .section-hdr.inline { border-bottom: 1px solid #1a1e36; }
  .action-row {
    width: 100%; display: grid; grid-template-columns: 170px 1fr; gap: .75rem;
    text-align: left; background: transparent; border: 0; border-top: 1px solid #171b31;
    color: #dfe2ff; padding: .65rem .9rem; cursor: pointer;
  }
  .action-row span { font-weight: 700; font-size: .78rem; }
  .action-row small { color: #8a91b8; font-size: .72rem; line-height: 1.35; }
  .action-row:hover { background: #171a2e; }
  .action-row.fail span { color: #ff8585; }
  .action-row.warn span { color: #f0b070; }

  .section     { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .section-hdr {
    display: flex; align-items: center; gap: .7rem;
    padding: .8rem 1rem; border-bottom: 1px solid #1a1e36;
    font-size: .875rem; font-weight: 600; flex-shrink: 0;
  }
  .pill      { font-size: .7rem; padding: .15rem .5rem; border-radius: 999px; background: #1c1f35; color: #6b7294; }
  /* Suggestions sit above the live log and must size to their content — reset
     the .section flex-grow/clip so cards aren't cut off. */
  .suggest-section { margin-bottom: 1rem; flex: 0 0 auto; overflow: visible; }
  .suggest-list { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: .6rem; padding: .8rem 1rem; }
  .suggest-card { position: relative; display: flex; gap: .5rem; background: #141626; border: 1px solid #232847; border-left: 3px solid #6c63ff; border-radius: 9px; padding: .7rem .85rem; }
  .suggest-card.review { border-left-color: #f0a060; }
  .suggest-title { font-size: .82rem; font-weight: 650; color: #c5c9e8; }
  .suggest-detail { font-size: .72rem; color: #8a91b8; margin-top: .28rem; line-height: 1.35; }
  .suggest-action { font-size: .72rem; color: #7d84c9; margin-top: .4rem; font-weight: 600; }
  .suggest-dismiss { position: absolute; top: .35rem; right: .45rem; background: transparent; border: 0; color: #4a4f70; font-size: 1rem; line-height: 1; cursor: pointer; }
  .suggest-dismiss:hover { color: #c5c9e8; }
  .pill-live { background: rgba(76,175,130,.15); color: #4caf82; }
  .filter-tabs { margin-left: auto; display: inline-flex; gap: .25rem; padding: .15rem; background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px; }
  .filter-tabs button {
    background: transparent; color: #7b82a8; border: 0; border-radius: 6px;
    padding: .22rem .5rem; font-size: .72rem; cursor: pointer;
  }
  .filter-tabs button.active { background: #262b4c; color: #f0f2ff; }
  .filter-tabs button:hover { color: #f0f2ff; }

  .log { flex: 1; overflow-y: auto; font-family: monospace; font-size: .78rem; max-height: 480px; }
  .log-row {
    display: grid; grid-template-columns: 72px 180px 130px 1fr;
    gap: .6rem; padding: .35rem 1rem; border-bottom: 1px solid #0e1020;
  }
  .log-row:hover { background: #1a1e36; }
  .log-time  { color: #6b7294; }
  .log-type  { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .log-agent { color: #6c63ff; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .log-data  { color: #6b7294; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .update-banner {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 16px 24px;
    margin-bottom: 24px;
    background: linear-gradient(135deg, rgba(126, 92, 255, 0.12), rgba(34, 196, 122, 0.12));
    border: 1px solid rgba(126, 92, 255, 0.35);
    border-radius: 12px;
    box-shadow: 0 8px 32px 0 rgba(126, 92, 255, 0.06);
    backdrop-filter: blur(8px);
    animation: fadeIn 0.4s ease-out;
  }
  @keyframes fadeIn {
    from { opacity: 0; transform: translateY(-8px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .update-banner-icon {
    font-size: 22px;
  }
  .update-banner-content {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .update-banner-content strong {
    font-size: 15px;
    color: #fff;
  }
  .update-banner-content span {
    font-size: 13px;
    color: #a0aec0;
  }
  .update-banner-actions {
    display: flex;
    gap: 12px;
  }
  .btn-sm {
    padding: 6px 14px;
    font-size: 12px;
  }
  .upgrading-overlay {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(10, 10, 15, 0.9);
    backdrop-filter: blur(16px);
    z-index: 10000;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: white;
  }
  .upgrading-spinner {
    width: 60px;
    height: 60px;
    border: 4px solid rgba(255, 255, 255, 0.1);
    border-top: 4px solid #7e5cff;
    border-radius: 50%;
    animation: spin 0.9s linear infinite;
    margin-bottom: 24px;
  }
  @keyframes spin {
    0% { transform: rotate(0deg); }
    100% { transform: rotate(360deg); }
  }
  .upgrading-text {
    font-size: 20px;
    font-weight: 600;
    margin-bottom: 12px;
    letter-spacing: -0.02em;
  }
  .upgrading-subtext {
    font-size: 13px;
    color: #8a91b8;
  }

  @media (max-width: 640px) {
    /* Event log: time+type on row 1, agent+data on row 2 */
    .log-row { grid-template-columns: 72px minmax(0, 1fr); row-gap: 0.1rem; }
    .log-data { grid-column: 1 / -1; white-space: normal; overflow-wrap: anywhere; }
    .filter-tabs { margin-left: 0; flex-wrap: wrap; }
    .section-hdr { flex-wrap: wrap; }
    .readiness-top { align-items: flex-start; flex-direction: column; }
    .readiness-actions { justify-content: flex-start; }
    .release-strip { flex-direction: column; }
    .slo-strip { flex-direction: column; align-items: flex-start; }
    .release-cmds { align-items: flex-start; min-width: 0; width: 100%; }
    .action-row { grid-template-columns: 1fr; gap: .25rem; }
  }
  .upgrade-err {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .upgrade-err .linkish {
    background: none;
    border: none;
    color: inherit;
    text-decoration: underline;
    cursor: pointer;
    font: inherit;
    padding: 0;
  }
</style>
