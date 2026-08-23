<script>
  import { onMount } from 'svelte'
  import TourButton from '../lib/TourButton.svelte'
  import ModelFields from '../lib/ModelFields.svelte'
  import { api } from '../lib/api.js'
  import { activeWorkspace, can } from '../lib/workspace.js'

  let loading = true, saving = false, error = '', info = ''
  let providers = []
  let modelsByProvider = {}, modelsLoading = {}, modelsError = {}, modelRequests = {}
  let defaultProvider = '', defaultModel = '', chatProvider = '', chatModel = ''
  let studioProvider = '', studioModel = '', reasonerProvider = '', reasonerModel = ''
  let studioPreset = '', buildUX = 'streamed', maxBuildTokens = 0, maxBuildCostUSD = 0
  let searchProvider = '', searchTimeout = '', searchAPIKey = '', searchKeySet = false
  let dailyUSD = 0, monthlyUSD = 0, dailyTokens = 0, concurrency = 0
  let costAlertThreshold = 0.8, costRows = []
  let sloWindow = '24h', sloMaxFailureRate = 0.1, sloMaxIncompleteRate = 0.05
  let sloMaxP95Duration = '5m', sloMinRuns = 10, opsAlertChannel = '', opsAlertDestination = '', opsAlertMinStatus = 'fail'
  let workspaceEnvironment = 'development', workspaceOwner = '', workspaceRegion = '', workspaceNotes = ''
  let securityIntentGate = 'prompt', toolTimeout = '30s', defaultMaxTurns = 15, maxAgentCallDepth = 5
  $: writable = can('config', 'write')
  $: isOwner = $activeWorkspace?.role === 'owner'
  $: usesNVIDIA = [defaultProvider, chatProvider, studioProvider, reasonerProvider].some((provider) => provider === 'nvidia')

  async function loadModels(providerID, force = false) {
    providerID = String(providerID || '').trim()
    if (!providerID) return []
    if (!force && modelsByProvider[providerID]) return modelsByProvider[providerID]
    if (!force && modelRequests[providerID]) return modelRequests[providerID]
    modelsLoading = { ...modelsLoading, [providerID]: true }
    const request = api.workspaceProviders.models(providerID)
      .then((res) => {
        const discovered = [...new Set((res.models || []).map((value) => String(value || '').trim()).filter(Boolean))]
        modelsByProvider = { ...modelsByProvider, [providerID]: discovered }
        modelsError = { ...modelsError, [providerID]: '' }
        return discovered
      })
      .catch((e) => {
        modelsError = { ...modelsError, [providerID]: e.message }
        return []
      })
      .finally(() => {
        modelsLoading = { ...modelsLoading, [providerID]: false }
        const next = { ...modelRequests }
        delete next[providerID]
        modelRequests = next
      })
    modelRequests = { ...modelRequests, [providerID]: request }
    return request
  }

  function discoverSelectedModels() {
    for (const providerID of new Set([defaultProvider, chatProvider, studioProvider, reasonerProvider].filter(Boolean))) {
      loadModels(providerID)
    }
  }

  async function load() {
    loading = true; error = ''
    try {
      const [cfg, providerCatalog] = await Promise.all([api.workspaceConfig.get(), api.workspaceProviders.list()])
      providers = Object.keys(providerCatalog.providers || cfg.llm?.providers || {}).sort()
      defaultProvider = cfg.llm?.default?.provider || cfg.llm?.default_provider || ''
      defaultModel = cfg.llm?.default?.model || cfg.llm?.providers?.[defaultProvider]?.model || ''
      chatProvider = cfg.llm?.chat?.provider || ''
      chatModel = cfg.llm?.chat?.model || ''
      studioProvider = cfg.llm?.studio?.provider || ''
      studioModel = cfg.llm?.studio?.model || ''
      reasonerProvider = cfg.llm?.reasoner?.provider || ''
      reasonerModel = cfg.llm?.reasoner?.model || ''
      studioPreset = cfg.llm?.studio?.preset || ''
      buildUX = cfg.llm?.studio?.build_ux || 'streamed'
      maxBuildTokens = cfg.llm?.studio?.max_build_tokens || 0
      maxBuildCostUSD = cfg.llm?.studio?.max_build_cost_usd || 0
      searchProvider = cfg.search?.provider || ''
      searchTimeout = cfg.search?.timeout || ''
      searchKeySet = !!cfg.search?.api_key_set
      costAlertThreshold = cfg.costs?.alert_threshold ?? 0.8
      costRows = Object.entries(cfg.costs?.pricing || {}).map(([selector, rates]) => ({ selector, input: rates?.input ?? 0, output: rates?.output ?? 0 }))
      sloWindow = cfg.ops?.slo_window || '24h'
      sloMaxFailureRate = cfg.ops?.max_failure_rate ?? 0.1
      sloMaxIncompleteRate = cfg.ops?.max_incomplete_rate ?? 0.05
      sloMaxP95Duration = cfg.ops?.max_p95_run_duration || '5m'
      sloMinRuns = cfg.ops?.min_runs_for_signal ?? 10
      opsAlertChannel = cfg.ops?.alert_channel || ''
      opsAlertDestination = cfg.ops?.alert_destination || ''
      opsAlertMinStatus = cfg.ops?.alert_min_status || 'fail'
      workspaceEnvironment = cfg.profile?.environment || 'development'
      workspaceOwner = cfg.profile?.owner || ''
      workspaceRegion = cfg.profile?.region || ''
      workspaceNotes = cfg.profile?.notes || ''
      securityIntentGate = cfg.security?.intent_gate || 'prompt'
      toolTimeout = cfg.runtime?.tool_timeout || '30s'
      defaultMaxTurns = cfg.runtime?.default_max_turns ?? 15
      maxAgentCallDepth = cfg.runtime?.max_agent_call_depth ?? 5
      discoverSelectedModels()
      if (isOwner) {
        const policy = await api.workspaceAdmin.policy()
        const p = policy.policy || {}
        dailyUSD = p.daily_usd || 0; monthlyUSD = p.monthly_usd || 0
        dailyTokens = p.daily_tokens || 0; concurrency = p.concurrency || 0
      }
    } catch (e) { error = e.message } finally { loading = false }
  }

  async function save() {
    saving = true; error = ''; info = ''
    try {
      await api.workspaceConfig.patch({
        llm: {
          default: { provider: defaultProvider, model: defaultModel },
          chat: { provider: chatProvider, model: chatModel },
          studio: { provider: studioProvider, model: studioModel, preset: studioPreset, build_ux: buildUX, max_build_tokens: Number(maxBuildTokens) || 0, max_build_cost_usd: Number(maxBuildCostUSD) || 0 },
          reasoner: { provider: reasonerProvider, model: reasonerModel },
        },
        search: { provider: searchProvider, timeout: searchTimeout, ...(searchAPIKey ? { api_key: searchAPIKey } : {}) },
        costs: {
          alert_threshold: Number(costAlertThreshold) || 0,
          pricing: Object.fromEntries(costRows.filter((row) => row.selector.trim()).map((row) => [row.selector.trim(), { input: Number(row.input) || 0, output: Number(row.output) || 0 }])),
        },
        ops: { slo_window: sloWindow, max_failure_rate: Number(sloMaxFailureRate) || 0, max_incomplete_rate: Number(sloMaxIncompleteRate) || 0, max_p95_run_duration: sloMaxP95Duration, min_runs_for_signal: Number(sloMinRuns) || 0, alert_channel: opsAlertChannel, alert_destination: opsAlertDestination, alert_min_status: opsAlertMinStatus },
        profile: { environment: workspaceEnvironment, owner: workspaceOwner, region: workspaceRegion, notes: workspaceNotes },
        security: { intent_gate: securityIntentGate },
        runtime: { tool_timeout: toolTimeout, default_max_turns: Number(defaultMaxTurns) || 0, max_agent_call_depth: Number(maxAgentCallDepth) || 0 },
      })
      if (isOwner) await api.workspaceAdmin.savePolicy({ daily_usd: Number(dailyUSD) || 0, monthly_usd: Number(monthlyUSD) || 0, daily_tokens: Number(dailyTokens) || 0, concurrency: Number(concurrency) || 0 })
      searchAPIKey = ''; info = 'Workspace configuration saved.'
      await load(); info = 'Workspace configuration saved.'
    } catch (e) { error = e.message } finally { saving = false }
  }

  onMount(load)

  function addCostRow() { costRows = [...costRows, { selector: '', input: 0, output: 0 }] }
  function removeCostRow(index) { costRows = costRows.filter((_, i) => i !== index) }
</script>

<div class="page">
  <header class="head"><div><span class="eyebrow">WORKSPACE CONFIGURATION</span><h1>Configuration</h1><p>Personal-mode controls that are safe to isolate to this workspace. Deployment policy remains the upper boundary.</p></div><TourButton /></header>
  {#if error}<div class="banner err">{error}</div>{/if}
  {#if info}<div class="banner ok">{info}</div>{/if}
  {#if loading}<div class="card">Loading workspace configuration…</div>{:else}
    <div class="grid">
      <section class="card wide"><div class="section-title"><div><h2>LLM</h2><p>Choose task-specific provider and model roles just as in Personal mode. Configure credentials and discover models on <a href="#providers">Providers &amp; models</a>.</p></div><span class="scope">WORKSPACE</span></div>
        {#if usesNVIDIA}<div class="provider-warning"><strong>NVIDIA hosted endpoints are intended for prototyping.</strong> Free Developer Program access can have model- and account-dependent token, rate, and availability limits. Set workspace budgets for predictable usage, and use an entitled or self-hosted provider for production workloads. <a href="https://docs.api.nvidia.com/nim/re/docs/run-anywhere" target="_blank" rel="noreferrer">NVIDIA deployment guidance ↗</a></div>{/if}
        <div class="model-grid">
          <div class="model"><h3>Default LLM</h3><ModelFields bind:provider={defaultProvider} bind:model={defaultModel} {providers} models={modelsByProvider[defaultProvider] || []} loading={!!modelsLoading[defaultProvider]} error={modelsError[defaultProvider] || ''} {writable} on:providerchange={(event) => loadModels(event.detail.provider)} on:refresh={(event) => loadModels(event.detail.provider, true)} /></div>
          <div class="model"><h3>Chat LLM</h3><ModelFields bind:provider={chatProvider} bind:model={chatModel} {providers} models={modelsByProvider[chatProvider] || []} loading={!!modelsLoading[chatProvider]} error={modelsError[chatProvider] || ''} {writable} optional on:providerchange={(event) => loadModels(event.detail.provider)} on:refresh={(event) => loadModels(event.detail.provider, true)} /></div>
          <div class="model"><h3>Studio LLM</h3><ModelFields bind:provider={studioProvider} bind:model={studioModel} {providers} models={modelsByProvider[studioProvider] || []} loading={!!modelsLoading[studioProvider]} error={modelsError[studioProvider] || ''} {writable} on:providerchange={(event) => loadModels(event.detail.provider)} on:refresh={(event) => loadModels(event.detail.provider, true)} /></div>
          <div class="model"><h3>Reasoner LLM</h3><ModelFields bind:provider={reasonerProvider} bind:model={reasonerModel} {providers} models={modelsByProvider[reasonerProvider] || []} loading={!!modelsLoading[reasonerProvider]} error={modelsError[reasonerProvider] || ''} {writable} optional on:providerchange={(event) => loadModels(event.detail.provider)} on:refresh={(event) => loadModels(event.detail.provider, true)} /></div>
        </div>
      </section>

      <section class="card"><h2>Studio behavior</h2><p>Control authoring quality, presentation, and per-build guardrails.</p>
        <label>Runtime intent<select bind:value={studioPreset} disabled={!writable}><option value="">Model default</option><option value="fast_local">Fast local</option><option value="reliable_local">Reliable local</option><option value="cloud_quality">Cloud quality</option></select></label>
        <label>Generate experience<select bind:value={buildUX} disabled={!writable}><option value="streamed">Streamed</option><option value="wizard">Wizard</option></select></label>
        <div class="two"><label>Max build tokens<input type="number" min="0" bind:value={maxBuildTokens} disabled={!writable} /></label><label>Max build cost (USD)<input type="number" min="0" step="0.01" bind:value={maxBuildCostUSD} disabled={!writable} /></label></div>
      </section>

      <section class="card"><h2>Web search</h2><p>The API key is encrypted in this workspace’s vault and is never returned.</p>
        <label>Provider<select bind:value={searchProvider} disabled={!writable}><option value="">Deployment default</option><option value="tavily">Tavily</option><option value="serper">Serper</option><option value="ollama">Ollama</option></select></label>
        <label>API key<input type="password" bind:value={searchAPIKey} placeholder={searchKeySet ? 'Stored — enter to replace' : 'Enter workspace search key'} disabled={!writable} /></label>
        <label>Timeout<input bind:value={searchTimeout} placeholder="30s" disabled={!writable} /></label>
      </section>

      {#if isOwner}<section class="card wide"><h2>Workspace LLM budgets</h2><p>Self-imposed limits can tighten deployment ceilings but can never raise them.</p>
        <div class="four"><label>Daily spend (USD)<input type="number" min="0" step="0.01" bind:value={dailyUSD} disabled={!writable} /></label><label>Monthly spend (USD)<input type="number" min="0" step="0.01" bind:value={monthlyUSD} disabled={!writable} /></label><label>Daily tokens<input type="number" min="0" bind:value={dailyTokens} disabled={!writable} /></label><label>Concurrent runs<input type="number" min="0" bind:value={concurrency} disabled={!writable} /></label></div>
      </section>{/if}

      <section class="card wide"><h2>Cost estimation</h2><p>Workspace-specific model pricing powers run metrics and budget alerts. Selectors use <code>provider/model</code> or <code>provider/*</code>.</p>
        <div class="cost-head"><label>Alert threshold<input type="number" min="0.01" max="1" step="0.05" bind:value={costAlertThreshold} disabled={!writable} /></label>{#if writable}<button class="secondary" on:click={addCostRow}>+ Add pricing row</button>{/if}</div>
        {#if costRows.length === 0}<p class="empty">No workspace pricing overrides. Deployment pricing is inherited.</p>{/if}
        {#each costRows as row, index}<div class="cost-row"><label>Selector<input bind:value={row.selector} placeholder="nvidia/*" disabled={!writable} /></label><label>Input $ / 1M tokens<input type="number" min="0" step="0.0001" bind:value={row.input} disabled={!writable} /></label><label>Output $ / 1M tokens<input type="number" min="0" step="0.0001" bind:value={row.output} disabled={!writable} /></label>{#if writable}<button class="remove" title="Remove pricing row" on:click={() => removeCostRow(index)}>Remove</button>{/if}</div>{/each}
      </section>

      <section class="card wide"><h2>Production SLOs</h2><p>Set workspace reliability thresholds and route operational alerts through a workspace channel.</p>
        <div class="four"><label>Window<input bind:value={sloWindow} placeholder="24h" disabled={!writable} /></label><label>Max failure rate<input type="number" min="0" max="1" step="0.01" bind:value={sloMaxFailureRate} disabled={!writable} /></label><label>Max incomplete rate<input type="number" min="0" max="1" step="0.01" bind:value={sloMaxIncompleteRate} disabled={!writable} /></label><label>P95 run duration<input bind:value={sloMaxP95Duration} placeholder="5m" disabled={!writable} /></label></div>
        <div class="four"><label>Minimum runs<input type="number" min="0" bind:value={sloMinRuns} disabled={!writable} /></label><label>Alert channel<input bind:value={opsAlertChannel} placeholder="slack" disabled={!writable} /></label><label>Alert destination<input bind:value={opsAlertDestination} placeholder="channel id" disabled={!writable} /></label><label>Alert threshold<select bind:value={opsAlertMinStatus} disabled={!writable}><option value="fail">fail only</option><option value="warn">warn or fail</option></select></label></div>
      </section>

      <section class="card"><h2>Workspace profile</h2><p>Context used by workspace readiness reports and support exports.</p>
        <label>Environment<select bind:value={workspaceEnvironment} disabled={!writable}><option value="local">local</option><option value="development">development</option><option value="staging">staging</option><option value="production">production</option></select></label>
        <div class="two"><label>Owner<input bind:value={workspaceOwner} placeholder="workspace team" disabled={!writable} /></label><label>Region<input bind:value={workspaceRegion} placeholder="us-central" disabled={!writable} /></label></div>
        <label>Notes<textarea rows="3" bind:value={workspaceNotes} placeholder="Customer, environment, or support context" disabled={!writable}></textarea></label>
      </section>

      <section class="card"><h2>Security &amp; runtime</h2><p>Workspace defaults remain bounded by deployment ceilings and per-agent restrictions.</p>
        <fieldset disabled={!writable}><legend>Intent gate</legend><label class="radio"><input type="radio" bind:group={securityIntentGate} value="off" />Off</label><label class="radio"><input type="radio" bind:group={securityIntentGate} value="prompt" />Prompt risky calls</label><label class="radio"><input type="radio" bind:group={securityIntentGate} value="deny" />Deny under injection</label></fieldset>
        <div class="two"><label>Tool timeout<input bind:value={toolTimeout} placeholder="30s" disabled={!writable} /></label><label>Default max turns<input type="number" min="0" max="100" bind:value={defaultMaxTurns} disabled={!writable} /></label></div>
        <label>Max agent-call depth<input type="number" min="0" max="50" bind:value={maxAgentCallDepth} disabled={!writable} /></label>
      </section>

      <section class="scope-note wide">
        <div><strong>Intentionally managed by the deployment administrator</strong><span>Executors and sandboxes, worker topology, filesystem paths, gateway logging, updates, host webhooks, and global plugin wiring affect every workspace and are not exposed here.</span></div>
        <a href="#workspace-admin">Workspace governance →</a>
      </section>
    </div>
    <div class="actions"><span>{writable ? 'Changes take effect without restarting the gateway.' : 'Your role has read-only access.'}</span><button class="primary" on:click={save} disabled={!writable || saving}>{saving ? 'Saving…' : 'Save changes'}</button></div>
  {/if}
</div>

<style>
  .page{max-width:1240px;margin:0 auto;padding:28px;color:#ececf8}.head{display:flex;justify-content:space-between;gap:20px;align-items:flex-start;margin-bottom:22px}.eyebrow{display:block;margin-bottom:8px;color:#72e6b5;font-size:10px;font-weight:800;letter-spacing:.16em}.head h1{margin:0;font-size:30px}.head p,.card p{color:#a9aec8;margin:6px 0 20px;line-height:1.5}.grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.card{background:#141728;border:1px solid #2b3049;border-radius:14px;padding:22px}.wide{grid-column:1/-1}.card h2{font-size:18px;margin:0}.card a,.scope-note a{color:#8b85ff}.provider-warning{margin:0 0 16px;padding:12px 14px;border:1px solid #665126;border-radius:9px;background:#2c2619;color:#e7ce8c;font-size:13px;line-height:1.5}.provider-warning a{color:#8bd9ff}.model-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.model{background:#0e1120;border:1px solid #272c46;border-radius:10px;padding:15px}.model h3{margin:0 0 12px;font-size:14px}.section-title{display:flex;justify-content:space-between;gap:20px}.scope{font-size:10px;letter-spacing:.14em;color:#72e6b5}.two,.four{display:grid;grid-template-columns:repeat(2,1fr);gap:12px}.four{grid-template-columns:repeat(4,1fr)}label{display:grid;gap:6px;color:#cdd1e8;font-size:13px;margin-top:12px}input,select,textarea{width:100%;box-sizing:border-box;background:#0d1020;color:#f2f3ff;border:1px solid #303655;border-radius:8px;padding:10px}.cost-head{display:flex;align-items:end;justify-content:space-between;gap:18px}.cost-head label{width:220px}.cost-row{display:grid;grid-template-columns:2fr 1fr 1fr auto;align-items:end;gap:12px}.secondary,.remove{border:1px solid #3a4060;border-radius:8px;background:#20243a;color:#e9eafd;padding:10px 13px;font-weight:650}.remove{color:#ff9caf;border-color:#713345;background:#311b27}.empty{padding:14px;border:1px dashed #343950;border-radius:8px;background:#101321}fieldset{border:1px solid #303655;border-radius:9px;margin:14px 0 0;padding:10px 12px}legend{padding:0 6px;color:#aeb3cf;font-size:12px}.radio{display:flex;align-items:center;gap:8px;margin:8px 0}.radio input{width:auto}.scope-note{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:16px 18px;border:1px dashed #363b57;border-radius:12px;background:#101321;color:#cfd3e8}.scope-note div{display:grid;gap:5px}.scope-note span{color:#8f95b2;font-size:12px;line-height:1.5}.scope-note a{white-space:nowrap;font-size:13px}.actions{display:flex;align-items:center;justify-content:flex-end;gap:20px;margin-top:18px;color:#9298b5}.primary{border:0;border-radius:9px;padding:11px 18px;color:white;font-weight:700;background:linear-gradient(90deg,#775cff,#20c887)}.primary:disabled{opacity:.5}.banner{padding:12px 15px;border-radius:9px;margin-bottom:15px}.err{background:#3a1d29;color:#ff9cb0}.ok{background:#12332b;color:#8ff0cb}@media(max-width:800px){.grid,.model-grid,.two,.four,.cost-row{grid-template-columns:1fr}.wide{grid-column:auto}.scope-note,.actions,.cost-head{align-items:stretch;flex-direction:column}.cost-head label{width:auto}}
</style>
