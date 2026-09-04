<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let agents = []
  let agentId = ''
  let sessionId = ''
  let trace = null
  let policy = null
  let status = null
  let statusError = ''
  let loading = false
  let error = ''
  let routeAgentId = ''
  let actionFilter = 'all'
  let copied = false

  const ACTION_ICON = {
    navigate: '🌐', click: '🖱', type: '⌨', extract: '📄', screenshot: '📸', other: '•',
  }

  function imageSrc(ref) {
    ref = String(ref || '').trim()
    if (ref.startsWith('data:image/') || ref.startsWith('http://') || ref.startsWith('https://')) return ref
    return ''
  }

  function screenshotSrc(step) {
    return step?.screenshot_url || imageSrc(step?.screenshot)
  }

  function routeParams() {
    const h = location.hash || ''
    const i = h.indexOf('?')
    return new URLSearchParams(i >= 0 ? h.slice(i + 1) : '')
  }

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      if (routeAgentId && agents.find((a) => a.id === routeAgentId)) {
        agentId = routeAgentId
      } else if (!agentId && agents.length) {
        agentId = agents[0].id
      }
      if (agentId) await load()
    } catch (e) { error = e.message }
  }

  async function loadStatus() {
    statusError = ''
    try {
      status = await api.browserStatus()
    } catch (e) {
      status = null
      statusError = e.message
    }
  }

  async function load() {
    if (!agentId) return
    loading = true
    error = ''
    trace = null
    policy = null
    try {
      const res = await api.browserTrace(agentId, sessionId.trim())
      trace = res.trace || null
      policy = res.policy || null
      if (res.enabled === false) error = 'Action logging is disabled, so no trace is available.'
    } catch (e) {
      error = e.message
    }
    loading = false
  }

  $: screenshotSteps = (trace?.steps || []).filter(s => s.screenshot)
  $: actionCounts = (trace?.steps || []).reduce((acc, s) => {
    acc[s.action || 'other'] = (acc[s.action || 'other'] || 0) + 1
    return acc
  }, {})
  $: visibleSteps = (trace?.steps || []).filter(s => {
    if (actionFilter === 'all') return true
    if (actionFilter === 'errors') return s.is_error
    return s.action === actionFilter
  })
  $: policyAction = policy?.browser_action || policy?.network || 'allow'
  $: policyTone = policyAction === 'deny' ? 'danger' : policyAction === 'prompt' ? 'warn' : 'ok'

  function deepLink() {
    if (!agentId) return ''
    const params = new URLSearchParams({ agent_id: agentId })
    if (sessionId.trim()) params.set('session_id', sessionId.trim())
    return `${location.origin}${location.pathname}#browser?${params.toString()}`
  }

  async function copyDeepLink() {
    const link = deepLink()
    if (!link) return
    try {
      await navigator.clipboard.writeText(link)
      copied = true
      setTimeout(() => copied = false, 1400)
    } catch (_) {
      error = 'Could not copy link.'
    }
  }

  function downloadTrace() {
    if (!trace) return
    const blob = new Blob([JSON.stringify(trace, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `browser-trace-${agentId || 'agent'}${sessionId ? '-' + sessionId : ''}.json`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  onMount(() => {
    const params = routeParams()
    routeAgentId = params.get('agent_id') || ''
    sessionId = params.get('session_id') || ''
    loadStatus()
    loadAgents()
  })
</script>

<div class="page">
  <div class="page-header">
    <h1>Browser Trace</h1>
    <button class="btn-secondary" on:click={load} disabled={loading || !agentId}>{loading ? 'Loading…' : '↺ Refresh'}</button>
        <TourButton />
    </div>

  <p class="intro">
    Replay an agent's browser automation — every navigate, click, type, extract and
    screenshot, reconstructed from the action log. Per-domain navigation is enforced
    by each agent's tool policy.
  </p>

  {#if status}
    <section class="readiness" class:ok={status.status === 'ok'} class:warn={status.status === 'warn'} class:fail={status.status === 'fail'}>
      <div class="ready-main">
        <div class="ready-score">{status.score}</div>
        <div>
          <strong>Automation readiness</strong>
          <p>{status.ready}/{status.total} checks ready. {status.sidecars?.length || 0} browser sidecar{(status.sidecars?.length || 0) === 1 ? '' : 's'} detected.</p>
        </div>
      </div>
      <div class="ready-checks">
        {#each status.checks || [] as check}
          <span class={check.status} title={check.detail}>{check.label}</span>
        {/each}
      </div>
    </section>
    <section class="browser-ops">
      <div class="ops-card {status.policy?.status || 'ok'}">
        <div class="ops-label">Policy Coverage</div>
        <strong>{status.policy?.status === 'warn' ? 'Needs review' : 'Governed'}</strong>
        <p>{status.policy?.detail || 'Browser policy coverage is available when agents and sidecars are loaded.'}</p>
        <div class="mini-stats">
          <span>{status.policy?.managed_agents || 0} managed</span>
          <span>{status.policy?.unmanaged_agents || 0} unmanaged</span>
        </div>
        {#if status.policy?.unmanaged_ids?.length}
          <div class="chips denied">{#each status.policy.unmanaged_ids as id}<span>{id}</span>{/each}</div>
        {/if}
      </div>
      <div class="ops-card">
        <div class="ops-label">Sidecars</div>
        <strong>{status.sidecars?.length || 0} detected</strong>
        {#if status.sidecars?.length}
          <div class="sidecar-list">
            {#each status.sidecars as srv}
              <div class="sidecar-row {srv.status}">
                <span>{srv.id}</span>
                <em>{srv.mode}{srv.headless ? ' · headless' : ''}</em>
                <small>{srv.tools?.length || 0} tool{(srv.tools?.length || 0) === 1 ? '' : 's'}</small>
              </div>
            {/each}
          </div>
        {:else}
          <p>No Playwright/Puppeteer/browser MCP sidecar is connected yet.</p>
        {/if}
      </div>
    </section>
  {:else if statusError}
    <div class="banner err">⚠ Browser readiness unavailable: {statusError}</div>
  {/if}

  <div class="controls">
    <label>
      Agent
      <select bind:value={agentId} on:change={load}>
        {#each agents as a}<option value={a.id}>{a.name || a.id}</option>{/each}
      </select>
    </label>
    <label>
      Session (optional)
      <input placeholder="filter by session id" bind:value={sessionId} on:keydown={(e) => e.key === 'Enter' && load()} />
    </label>
  </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}

  {#if trace}
    {#if policy}
      <section class="policy-panel" aria-label="Browser automation policy">
        <div class="policy-main">
          <div class="policy-eyebrow">Browser Policy</div>
          <div class="policy-title">
            <span class:ok={policyTone === 'ok'} class:warn={policyTone === 'warn'} class:danger={policyTone === 'danger'}>
              {policyAction}
            </span>
            {#if policy.requires_approval}<em>approval required</em>{/if}
            {#if !policy.enabled}<em>default</em>{/if}
          </div>
          <p>{policy.detail}</p>
        </div>
        <div class="policy-lists">
          <div>
            <strong>Allowed Domains</strong>
            {#if policy.allow_domains?.length}
              <div class="chips">{#each policy.allow_domains as d}<span>{d}</span>{/each}</div>
            {:else}
              <small>Any domain unless denied</small>
            {/if}
          </div>
          <div>
            <strong>Denied Domains</strong>
            {#if policy.deny_domains?.length}
              <div class="chips denied">{#each policy.deny_domains as d}<span>{d}</span>{/each}</div>
            {:else}
              <small>No explicit deny list</small>
            {/if}
          </div>
        </div>
      </section>
    {/if}

    <div class="summary">
      <div class="stat"><div class="val">{trace.steps?.length || 0}</div><div class="lbl">Steps</div></div>
      <div class="stat"><div class="val">{trace.navigations || 0}</div><div class="lbl">Navigations</div></div>
      <div class="stat"><div class="val">{screenshotSteps.length}</div><div class="lbl">Screenshots</div></div>
      {#if trace.last_url}
        <div class="stat wide"><div class="val url">{trace.last_url}</div><div class="lbl">Last URL</div></div>
      {/if}
    </div>

    <div class="trace-toolbar">
      <div class="filter-group" aria-label="Filter browser trace">
        <button class:on={actionFilter === 'all'} on:click={() => actionFilter = 'all'}>All</button>
        <button class:on={actionFilter === 'navigate'} on:click={() => actionFilter = 'navigate'}>Navigate {actionCounts.navigate || ''}</button>
        <button class:on={actionFilter === 'click'} on:click={() => actionFilter = 'click'}>Click {actionCounts.click || ''}</button>
        <button class:on={actionFilter === 'type'} on:click={() => actionFilter = 'type'}>Type {actionCounts.type || ''}</button>
        <button class:on={actionFilter === 'extract'} on:click={() => actionFilter = 'extract'}>Extract {actionCounts.extract || ''}</button>
        <button class:on={actionFilter === 'screenshot'} on:click={() => actionFilter = 'screenshot'}>Screenshots {actionCounts.screenshot || ''}</button>
        <button class:on={actionFilter === 'errors'} on:click={() => actionFilter = 'errors'}>Errors</button>
      </div>
      <div class="trace-actions">
        <button on:click={copyDeepLink} disabled={!agentId}>{copied ? 'Copied' : 'Copy link'}</button>
        <button on:click={downloadTrace}>Export JSON</button>
      </div>
    </div>

    {#if screenshotSteps.length}
      <section class="shot-gallery" aria-label="Screenshot gallery">
        <div class="gallery-head">
          <strong>Screenshot Gallery</strong>
          <span>{screenshotSteps.length} capture{screenshotSteps.length === 1 ? '' : 's'}</span>
        </div>
        <div class="gallery-grid">
          {#each screenshotSteps as s}
            <a class="shot-card" href={screenshotSrc(s) || undefined} target="_blank" rel="noreferrer" title={s.screenshot}>
              {#if screenshotSrc(s)}
                <img src={screenshotSrc(s)} alt="Browser screenshot step {s.seq}" />
              {:else}
                <div class="shot-path">Screenshot path<br><code>{s.screenshot}</code></div>
              {/if}
              <span>Step {s.seq} · {s.tool}</span>
            </a>
          {/each}
        </div>
      </section>
    {/if}

    {#if trace.steps && trace.steps.length}
      <div class="timeline">
        {#each visibleSteps as s}
          <div class="step" class:err={s.is_error}>
            <div class="step-seq">{s.seq}</div>
            <div class="step-icon">{ACTION_ICON[s.action] || '•'}</div>
            <div class="step-body">
              <div class="step-head">
                <span class="step-action">{s.action}</span>
                <span class="step-tool">{s.tool}</span>
                {#if s.is_error}<span class="step-badge">failed</span>{/if}
              </div>
              {#if s.url}<div class="step-detail">{s.url}</div>{/if}
              {#if s.target}<div class="step-detail dim">{s.target}</div>{/if}
              {#if s.output}<div class="step-output">{s.output}</div>{/if}
              {#if s.screenshot}
                <div class="screenshot-ref">
                  {#if screenshotSrc(s)}
                    <img src={screenshotSrc(s)} alt="Browser screenshot" />
                  {:else}
                    <span>Screenshot</span><code>{s.screenshot}</code>
                  {/if}
                </div>
              {/if}
            </div>
            {#if s.at}<div class="step-time">{new Date(s.at).toLocaleTimeString()}</div>{/if}
          </div>
        {/each}
      </div>
      {#if visibleSteps.length === 0}
        <div class="empty">No steps match this filter.</div>
      {/if}
    {:else}
      <div class="empty">No browser steps found for this selection. Run a browser-automation agent, then refresh.</div>
    {/if}
  {/if}
</div>

<style>
  .intro { color: #8a91b8; font-size: .85rem; max-width: 720px; margin: 0 0 1rem; line-height: 1.5; }
  .controls { display: flex; gap: 1rem; flex-wrap: wrap; margin-bottom: 1rem; }
  .controls label { display: flex; flex-direction: column; gap: .3rem; font-size: .72rem; color: #6b7294; }
  .controls select, .controls input { background: #0e1020; color: #d7dcf5; border: 1px solid #1a1e36; border-radius: 7px; padding: .4rem .55rem; font-size: .82rem; min-width: 220px; }
  .banner.err { background: rgba(240,96,96,.08); border: 1px solid rgba(240,96,96,.4); color: #f0a0a0; border-radius: 8px; padding: .6rem .8rem; margin-bottom: 1rem; }
  .readiness {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap;
    background: #111426; border: 1px solid #20243d; border-radius: 9px; padding: .8rem; margin-bottom: 1rem;
  }
  .readiness.ok { border-color: rgba(69, 217, 139, .28); }
  .readiness.warn { border-color: rgba(240, 188, 96, .32); }
  .readiness.fail { border-color: rgba(240, 96, 96, .35); }
  .ready-main { display: flex; align-items: center; gap: .75rem; min-width: min(100%, 320px); }
  .ready-score { width: 2.7rem; height: 2.7rem; display: grid; place-items: center; border-radius: 8px; background: #171a2d; color: #d7dcf5; font-weight: 800; }
  .ready-main strong { display: block; color: #c5c9e8; font-size: .86rem; }
  .ready-main p { margin: .18rem 0 0; color: #8a91b8; font-size: .74rem; }
  .ready-checks { display: flex; flex-wrap: wrap; gap: .35rem; }
  .ready-checks span { border: 1px solid #2b3152; border-radius: 999px; padding: .16rem .48rem; font-size: .68rem; color: #9aa0c8; cursor: help; }
  .ready-checks span.ok { color: #71e3a1; background: rgba(69, 217, 139, .08); border-color: rgba(69, 217, 139, .28); }
  .ready-checks span.warn { color: #f0c778; background: rgba(240, 188, 96, .08); border-color: rgba(240, 188, 96, .3); }
  .ready-checks span.fail { color: #f0a0a0; background: rgba(240, 96, 96, .08); border-color: rgba(240, 96, 96, .35); }
  .browser-ops { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: .75rem; margin: -.35rem 0 1rem; }
  .ops-card { background: #111426; border: 1px solid #20243d; border-radius: 9px; padding: .75rem; min-width: 0; }
  .ops-card.warn { border-color: rgba(240, 188, 96, .34); }
  .ops-card.fail { border-color: rgba(240, 96, 96, .35); }
  .ops-label { font-size: .62rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em; margin-bottom: .28rem; }
  .ops-card strong { display: block; color: #d7dcf5; font-size: .86rem; margin-bottom: .32rem; }
  .ops-card p { margin: 0 0 .55rem; color: #8a91b8; font-size: .76rem; line-height: 1.42; }
  .mini-stats { display: flex; gap: .35rem; flex-wrap: wrap; margin-bottom: .45rem; }
  .mini-stats span { border: 1px solid #252a46; background: #15182a; color: #9aa0c8; border-radius: 999px; padding: .13rem .45rem; font-size: .68rem; }
  .sidecar-list { display: flex; flex-direction: column; gap: .35rem; }
  .sidecar-row { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: .5rem; align-items: center; border: 1px solid #252a46; background: #15182a; border-radius: 7px; padding: .45rem .55rem; }
  .sidecar-row.ok { border-color: rgba(69, 217, 139, .24); }
  .sidecar-row.warn { border-color: rgba(240, 188, 96, .3); }
  .sidecar-row span { color: #d7dcf5; font-size: .74rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sidecar-row em { color: #8a91b8; font-style: normal; font-size: .68rem; }
  .sidecar-row small { color: #6b7294; font-size: .66rem; white-space: nowrap; }
  .policy-panel {
    display: grid; grid-template-columns: minmax(260px, 1fr) minmax(260px, 1.25fr); gap: 1rem;
    background: #111426; border: 1px solid #20243d; border-radius: 9px; padding: .85rem; margin-bottom: 1rem;
  }
  .policy-eyebrow { font-size: .62rem; text-transform: uppercase; letter-spacing: .08em; color: #6b7294; margin-bottom: .25rem; }
  .policy-title { display: flex; align-items: center; gap: .45rem; color: #c5c9e8; font-weight: 750; text-transform: capitalize; }
  .policy-title span { border: 1px solid #2b3152; border-radius: 999px; padding: .12rem .5rem; font-size: .76rem; }
  .policy-title span.ok { color: #71e3a1; background: rgba(69, 217, 139, .08); border-color: rgba(69, 217, 139, .28); }
  .policy-title span.warn { color: #f0c778; background: rgba(240, 188, 96, .08); border-color: rgba(240, 188, 96, .3); }
  .policy-title span.danger { color: #f0a0a0; background: rgba(240, 96, 96, .08); border-color: rgba(240, 96, 96, .35); }
  .policy-title em { color: #8a91b8; font-style: normal; font-size: .7rem; background: #171a2d; border-radius: 999px; padding: .1rem .45rem; }
  .policy-main p { margin: .45rem 0 0; color: #9aa0c8; font-size: .78rem; line-height: 1.45; }
  .policy-lists { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .7rem; }
  .policy-lists strong { display: block; color: #c5c9e8; font-size: .72rem; margin-bottom: .35rem; }
  .policy-lists small { color: #6b7294; font-size: .72rem; }
  .chips { display: flex; flex-wrap: wrap; gap: .3rem; }
  .chips span { color: #bfe8d1; background: rgba(69, 217, 139, .08); border: 1px solid rgba(69, 217, 139, .24); border-radius: 999px; padding: .12rem .45rem; font-size: .68rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chips.denied span { color: #f0a0a0; background: rgba(240, 96, 96, .08); border-color: rgba(240, 96, 96, .25); }
  .summary { display: flex; gap: .6rem; flex-wrap: wrap; margin-bottom: 1rem; }
  .stat { background: #141626; border: 1px solid #1a1e36; border-radius: 9px; padding: .6rem .85rem; min-width: 110px; }
  .stat.wide { flex: 1; min-width: 240px; }
  .val { font-size: 1.2rem; font-weight: 750; color: #c5c9e8; }
  .val.url { font-size: .82rem; font-weight: 600; word-break: break-all; }
  .lbl { font-size: .64rem; text-transform: uppercase; letter-spacing: .05em; color: #6b7294; margin-top: .3rem; }
  .trace-toolbar { display: flex; align-items: center; justify-content: space-between; gap: .8rem; flex-wrap: wrap; margin-bottom: 1rem; }
  .filter-group, .trace-actions { display: flex; gap: .35rem; flex-wrap: wrap; }
  .filter-group button, .trace-actions button {
    border: 1px solid #252a46; background: #15182a; color: #c5c9e8;
    border-radius: 7px; padding: .36rem .55rem; font-size: .74rem; cursor: pointer;
  }
  .filter-group button.on { border-color: #6c63ff; background: rgba(108,99,255,.16); color: #e8eaf6; }
  .trace-actions button:disabled { opacity: .5; cursor: not-allowed; }
  .shot-gallery { margin-bottom: 1rem; background: #111426; border: 1px solid #20243d; border-radius: 9px; padding: .75rem; }
  .gallery-head { display: flex; align-items: baseline; justify-content: space-between; gap: .75rem; margin-bottom: .65rem; }
  .gallery-head strong { color: #c5c9e8; font-size: .86rem; }
  .gallery-head span { color: #6b7294; font-size: .72rem; }
  .gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: .6rem; }
  .shot-card { display: flex; flex-direction: column; gap: .4rem; color: inherit; text-decoration: none; background: #15182a; border: 1px solid #252a46; border-radius: 8px; padding: .5rem; min-width: 0; }
  .shot-card:hover { border-color: #6c63ff; }
  .shot-card img { width: 100%; aspect-ratio: 16 / 10; object-fit: cover; border-radius: 6px; border: 1px solid #20243d; background: #0a0c17; }
  .shot-card span { color: #8a91b8; font-size: .7rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .shot-path { min-height: 98px; display: flex; flex-direction: column; justify-content: center; gap: .25rem; color: #8a91b8; font-size: .72rem; line-height: 1.35; }
  .shot-path code { color: #c5c9e8; word-break: break-all; }
  .timeline { display: flex; flex-direction: column; gap: .4rem; }
  .step { display: flex; align-items: center; gap: .7rem; background: #141626; border: 1px solid #1a1e36; border-left: 3px solid #6c63ff; border-radius: 9px; padding: .55rem .8rem; }
  .step.err { border-left-color: #f06060; }
  .step-seq { font-size: .72rem; color: #6b7294; width: 1.6rem; text-align: right; }
  .step-icon { font-size: 1rem; width: 1.4rem; text-align: center; }
  .step-body { flex: 1; min-width: 0; }
  .step-head { display: flex; align-items: center; gap: .5rem; }
  .step-action { font-size: .82rem; font-weight: 650; color: #c5c9e8; text-transform: capitalize; }
  .step-tool { font-size: .7rem; color: #6b7294; }
  .step-badge { font-size: .62rem; color: #f0a0a0; background: rgba(240,96,96,.12); border-radius: 999px; padding: .05rem .4rem; }
  .step-detail { font-size: .74rem; color: #9aa0c8; margin-top: .2rem; word-break: break-all; }
  .step-detail.dim { color: #6b7294; }
  .step-output { margin-top: .35rem; color: #b7bddb; background: rgba(255,255,255,.035); border: 1px solid #20243d; border-radius: 7px; padding: .42rem .55rem; font-size: .74rem; line-height: 1.45; white-space: pre-wrap; word-break: break-word; }
  .screenshot-ref { margin-top: .45rem; display: flex; align-items: center; gap: .45rem; color: #8a91b8; font-size: .7rem; }
  .screenshot-ref code { color: #c5c9e8; word-break: break-all; }
  .screenshot-ref img { max-width: min(420px, 100%); max-height: 240px; border-radius: 8px; border: 1px solid #20243d; background: #0a0c17; object-fit: contain; }
  .step-time { font-size: .68rem; color: #6b7294; white-space: nowrap; }
  .empty { color: #6b7294; font-size: .85rem; padding: 2rem; text-align: center; }
  @media (max-width: 820px) {
    .policy-panel, .policy-lists { grid-template-columns: 1fr; }
  }
</style>
