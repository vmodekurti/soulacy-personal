<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { rowsFromSettings, settingsPatchFromRows } from '../lib/pluginsettings.js'
  import { waitForGateway, waitingMessage, timeoutMessage, UPGRADE_BUDGET } from '../lib/gatewaywait.js'
  import { versionSummary, lastCheckedLabel } from '../lib/versioninfo.js'

  let config   = null
  let loading  = true
  let saving   = false
  let error    = ''
  let saved    = false
  let writable = false
  let restarting = false
  let downloadingSupport = false
  let auditLoading = false
  let auditError = ''
  let adminAudit = null

  let updateInfo = null
  let upgrading = false
  let upgradeMessage = ''
  let upgradeError = ''
  let checkingUpdates = false
  $: version = versionSummary(updateInfo)
  $: lastChecked = lastCheckedLabel(updateInfo && updateInfo.last_check_time)

  async function recheckUpdates() {
    if (checkingUpdates) return
    checkingUpdates = true
    try { await api.updates.check() } catch (_) { /* the status call below reports it */ }
    try { updateInfo = await api.updates.status() } catch { updateInfo = null }
    checkingUpdates = false
  }


  // Editable fields
  let logLevel  = 'info'
  let logFormat = 'console'
  let logFile   = ''
  let pythonBin = 'python3'
  let toolTimeout = '30s'
  let executorBackend = 'process'
  let executorWorkers = 4
  let dockerImage = 'python:3.12-slim'
  let dockerNetwork = 'none'
  let dockerVolumes = ''
  let sshHost = ''
  let sshUser = ''
  let sshPythonBin = 'python3'
  let sshIdentity = ''
  let sshIdentityCredential = ''
  let cloudPreset = ''
  let cloudTarget = ''
  let cloudCLI = ''
  let executorReadiness = null
  let executorError = ''
  let executorLoading = false
  let maxTurns    = 20
  let maxSessions = 100
  let maxAgentCallDepth = 5
  let defaultBudgetTokens = 500000
  let defaultBudgetCalls = 50
  let maxBudgetTokens = 2000000
  let maxBudgetCalls = 200
  let defaultProvider = ''
  let providerOptions = []   // configured llm.providers names (for the dropdown)
  let providerDefaults = {}   // provider id -> configured default model
  let modelsByProvider = {}
  let modelsLoading = {}
  let modelsError = {}
  let studioProvider = ''    // llm.studio override (Studio compiler)
  let studioModel = ''
  let reasonerProvider = ''  // fallback for reasoning agents without an assignment
  let reasonerModel = ''
  let searchProvider = 'ollama'
  let searchApiKey = ''
  let dailyBudgetUSD = ''
  let monthlyBudgetUSD = ''
  let costAlertThreshold = 0.8
  let costRows = []
  let sloWindow = '24h'
  let sloMaxFailureRate = 0.1
  let sloMaxIncompleteRate = 0.05
  let sloMaxP95RunDuration = '5m'
  let sloMinRunsForSignal = 3
  let opsAlertChannel = ''
  let opsAlertTo = ''
  let opsAlertMinStatus = 'fail'
  let deploymentProfile = 'local'
  let deploymentOwner = ''
  let deploymentRegion = ''
  let deploymentNotes = ''
  let agentDirs = ''
  let skillDirs = ''
  // F-GUI-6 — Cohort F: workspace-default enforcement mode for the tool-call
  // intent gate (S3). This is the same string persisted on each agent's
  // `security.intent_gate` in SOUL.yaml; we surface it here as a workspace
  // convention + guidance. Values match internal/intent/intent.go Mode:
  //   ''       — unset (fall back to prompt at runtime)
  //   'off'    — bypass the gate (advanced operators only)
  //   'prompt' — (default) confirm risky calls under High-severity injection
  //   'deny'   — hard-deny risky calls under injection influence (production)
  let securityIntentGate = ''

  // Plugin settings editor (Story 18): pid → editable rows; originals kept
  // for type-safe round-trips. Secrets arrive redacted as '***' and the
  // server skips those placeholders on PATCH, so saving never clobbers
  // real values on disk.
  let pluginRows = {}
  let pluginOriginals = {}
  let newPluginId = ''

  function seedPluginEditor(pc) {
    pluginOriginals = pc || {}
    pluginRows = {}
    for (const [pid, settings] of Object.entries(pluginOriginals)) {
      pluginRows[pid] = rowsFromSettings(settings)
    }
  }

  function addRow(pid) {
    pluginRows[pid] = [...pluginRows[pid], { key: '', value: '' }]
  }

  function removeRow(pid, idx) {
    pluginRows[pid] = pluginRows[pid].filter((_, i) => i !== idx)
  }

  function addPlugin() {
    const pid = newPluginId.trim()
    if (!pid || pluginRows[pid]) return
    pluginOriginals = { ...pluginOriginals, [pid]: {} }
    pluginRows = { ...pluginRows, [pid]: [{ key: '', value: '' }] }
    newPluginId = ''
  }

  function pluginsConfigPatch() {
    const patch = {}
    for (const [pid, rows] of Object.entries(pluginRows)) {
      patch[pid] = settingsPatchFromRows(pluginOriginals[pid], rows)
    }
    return patch
  }

  function seedCostEditor(pricing = {}) {
    costRows = Object.entries(pricing || {})
      .map(([selector, price]) => ({
        selector,
        input: price?.input_per_mtok ?? '',
        output: price?.output_per_mtok ?? '',
      }))
      .sort((a, b) => a.selector.localeCompare(b.selector))
  }

  function addCostRow() {
    costRows = [...costRows, { selector: '', input: '', output: '' }]
  }

  function removeCostRow(idx) {
    costRows = costRows.filter((_, i) => i !== idx)
  }

  function costsPatch() {
    const pricing = {}
    for (const row of costRows) {
      const selector = (row.selector || '').trim()
      if (!selector) continue
      pricing[selector] = {
        input_per_mtok: Number(row.input || 0),
        output_per_mtok: Number(row.output || 0),
      }
    }
    return {
      daily_budget_usd: Number(dailyBudgetUSD || 0),
      monthly_budget_usd: Number(monthlyBudgetUSD || 0),
      alert_threshold: Number(costAlertThreshold || 0),
      pricing,
    }
  }

  async function loadProviderRegistry(configured = []) {
    const ids = new Set(configured)
    const defaults = {}
    try {
      const res = await api.providers.list()
      for (const id of (res.registered || [])) ids.add(id)
      for (const id of (res.known || [])) ids.add(id)
      for (const [id, cfg] of Object.entries(res.providers || {})) {
        ids.add(id)
        if (cfg?.model) defaults[id] = cfg.model
      }
    } catch (_) {
      // Config remains usable with the static config.yaml provider list.
    }
    providerDefaults = defaults
    providerOptions = [...ids].filter(Boolean).sort()
  }

  async function loadModels(providerId) {
    if (!providerId || modelsByProvider[providerId] || modelsLoading[providerId]) return
    modelsLoading = { ...modelsLoading, [providerId]: true }
    try {
      const res = await api.providers.models(providerId)
      modelsByProvider = { ...modelsByProvider, [providerId]: res.models || [] }
      modelsError = { ...modelsError, [providerId]: '' }
    } catch (e) {
      modelsByProvider = { ...modelsByProvider, [providerId]: [] }
      modelsError = { ...modelsError, [providerId]: e.message }
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  function effectiveRoleProvider(roleProvider) {
    return roleProvider || defaultProvider
  }

  function modelOptions(providerId, current) {
    const list = modelsByProvider[providerId] || []
    const out = []
    if (current && !out.includes(current)) out.push(current)
    for (const m of list) {
      if (m && !out.includes(m)) out.push(m)
    }
    const def = providerDefaults[providerId]
    if (def && !out.includes(def)) out.unshift(def)
    return out
  }

  function pickProviderDefault(providerId, setter) {
    if (providerId && providerDefaults[providerId]) setter(providerDefaults[providerId])
  }

  async function loadExecutors() {
    executorLoading = true
    executorError = ''
    try {
      executorReadiness = await api.executors()
    } catch (e) {
      executorReadiness = null
      executorError = e.message
    } finally {
      executorLoading = false
    }
  }

  async function loadAdminAudit() {
    auditLoading = true
    auditError = ''
    try {
      adminAudit = await api.admin.audit(25)
    } catch (e) {
      adminAudit = null
      auditError = e.message
    } finally {
      auditLoading = false
    }
  }

  async function load() {
    loading = true
    error   = ''
    try {
      config = await api.config.get()
      writable = config._meta?.writable ?? false
      // Populate editable fields
      logLevel        = config.log?.level      || 'info'
      logFormat       = config.log?.format     || 'console'
      logFile         = config.log?.file       || ''
      pythonBin       = config.runtime?.python_bin    || 'python3'
      toolTimeout     = config.runtime?.tool_timeout  || '30s'
      executorBackend = config.executor?.backend || 'process'
      executorWorkers = config.executor?.workers || 4
      dockerImage     = config.executor?.docker_image || 'python:3.12-slim'
      dockerNetwork   = config.executor?.docker_network || 'none'
      dockerVolumes   = (config.executor?.docker_volumes || []).join('\n')
      sshHost         = config.executor?.ssh_host || ''
      sshUser         = config.executor?.ssh_user || ''
      sshPythonBin    = config.executor?.ssh_python_bin || 'python3'
      sshIdentity     = config.executor?.ssh_identity || ''
      sshIdentityCredential = config.executor?.ssh_identity_credential || ''
      cloudPreset     = config.executor?.cloud_preset || ''
      cloudTarget     = config.executor?.cloud_target || ''
      cloudCLI        = config.executor?.cloud_cli || ''
      maxTurns        = config.runtime?.default_max_turns       || 20
      maxSessions     = config.runtime?.max_concurrent_sessions || 100
      maxAgentCallDepth = config.runtime?.max_agent_call_depth || 5
      defaultBudgetTokens = config.runtime?.default_budget?.max_tokens ?? 500000
      defaultBudgetCalls = config.runtime?.default_budget?.max_llm_calls ?? 50
      maxBudgetTokens = config.runtime?.max_budget?.max_tokens ?? 2000000
      maxBudgetCalls = config.runtime?.max_budget?.max_llm_calls ?? 200
      defaultProvider = config.llm?.default_provider || ''
      providerOptions = Object.keys(config.llm?.providers || {})
      // Make sure the current value is selectable even if it isn't a
      // configured provider block (e.g. set manually in config.yaml).
      if (defaultProvider && !providerOptions.includes(defaultProvider)) {
        providerOptions = [defaultProvider, ...providerOptions]
      }
      studioProvider   = config.llm?.studio?.provider || ''
      studioModel      = config.llm?.studio?.model || ''
      reasonerProvider = config.llm?.reasoner?.provider || ''
      reasonerModel    = config.llm?.reasoner?.model || ''
      await loadProviderRegistry(providerOptions)
      searchProvider  = config.search?.provider || 'ollama'
      searchApiKey    = config.search?.api_key || ''
      dailyBudgetUSD = config.costs?.daily_budget_usd || ''
      monthlyBudgetUSD = config.costs?.monthly_budget_usd || ''
      costAlertThreshold = config.costs?.alert_threshold || 0.8
      sloWindow = config.ops?.slo_window || '24h'
      sloMaxFailureRate = config.ops?.max_failure_rate || 0.1
      sloMaxIncompleteRate = config.ops?.max_incomplete_rate || 0.05
      sloMaxP95RunDuration = config.ops?.max_p95_run_duration || '5m'
      sloMinRunsForSignal = config.ops?.min_runs_for_signal || 3
      opsAlertChannel = config.ops?.alert_channel || ''
      opsAlertTo = config.ops?.alert_to || ''
      opsAlertMinStatus = config.ops?.alert_min_status || 'fail'
      deploymentProfile = config.deployment?.profile || 'local'
      deploymentOwner = config.deployment?.owner || ''
      deploymentRegion = config.deployment?.region || ''
      deploymentNotes = config.deployment?.notes || ''
      securityIntentGate = config.security?.intent_gate || ''
      seedCostEditor(config.costs?.pricing)
      agentDirs       = (config.agent_dirs || []).join('\n')
      skillDirs       = (config.skill_dirs || []).join('\n')
      seedPluginEditor(config.plugins_config)
      await Promise.all([loadExecutors(), loadAdminAudit()])
    } catch (e) {
      error = e.message
    } finally {
      loading = false
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


  async function save() {
    saving = true
    error  = ''
    saved  = false
    try {
      const patch = {
        runtime: {
          python_bin:              pythonBin,
          tool_timeout:            toolTimeout,
          default_max_turns:       Number(maxTurns),
          max_concurrent_sessions: Number(maxSessions),
          max_agent_call_depth:    Number(maxAgentCallDepth),
          default_budget: {
            max_tokens: Number(defaultBudgetTokens || 0),
            max_llm_calls: Number(defaultBudgetCalls || 0),
          },
          max_budget: {
            max_tokens: Number(maxBudgetTokens || 0),
            max_llm_calls: Number(maxBudgetCalls || 0),
          },
        },
        executor: {
          backend: executorBackend,
          workers: Number(executorWorkers || 0),
          docker_image: dockerImage,
          docker_network: dockerNetwork,
          docker_volumes: dockerVolumes.split('\n').map(s => s.trim()).filter(Boolean),
          ssh_host: sshHost,
          ssh_user: sshUser,
          ssh_python_bin: sshPythonBin,
          ssh_identity: sshIdentity,
          ssh_identity_credential: sshIdentityCredential,
          cloud_preset: cloudPreset,
          cloud_target: cloudTarget,
          cloud_cli: cloudCLI,
        },
        llm: {
          default_provider: defaultProvider,
          // Empty provider/model = fall back to the default (server-side).
          studio:   { provider: studioProvider,   model: studioModel },
          reasoner: { provider: reasonerProvider, model: reasonerModel },
        },
        // api_key of '***' (redacted placeholder) is skipped server-side, so
        // saving without retyping the key never clobbers the real one on disk.
        search: { provider: searchProvider, api_key: searchApiKey },
        costs: costsPatch(),
        ops: {
          slo_window: sloWindow,
          max_failure_rate: Number(sloMaxFailureRate || 0),
          max_incomplete_rate: Number(sloMaxIncompleteRate || 0),
          max_p95_run_duration: sloMaxP95RunDuration,
          min_runs_for_signal: Number(sloMinRunsForSignal || 0),
          alert_channel: opsAlertChannel,
          alert_to: opsAlertTo,
          alert_min_status: opsAlertMinStatus,
        },
        deployment: {
          profile: deploymentProfile,
          owner: deploymentOwner,
          region: deploymentRegion,
          notes: deploymentNotes,
        },
        // F-GUI-6 — Cohort F workspace-default intent-gate mode. Cohort F
        // backend enforcement reads Security.IntentGate per agent; this key
        // records the workspace policy the operator wants to apply and is
        // consumed by future backend work + the doctor's advisory copy.
        security: { intent_gate: securityIntentGate || '' },
        log: { level: logLevel, format: logFormat, file: logFile },
        agent_dirs: agentDirs.split('\n').map(s => s.trim()).filter(Boolean),
        skill_dirs: skillDirs.split('\n').map(s => s.trim()).filter(Boolean),
      }
      const pcPatch = pluginsConfigPatch()
      if (Object.keys(pcPatch).length > 0) patch.plugins_config = pcPatch
      const res = await api.config.patch(patch)
      config = res.config
      seedPluginEditor(config.plugins_config)
      await loadExecutors()
      await loadAdminAudit()
      saved  = true
      setTimeout(() => { saved = false }, 3000)
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function restartGateway() {
    restarting = true
    error = ''
    try {
      await api.admin.restart()
      saved = false
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
    }
  }

  async function downloadSupportBundle() {
    downloadingSupport = true
    error = ''
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
    } catch (e) {
      error = e.message
    } finally {
      downloadingSupport = false
    }
  }

  $: loadModels(effectiveRoleProvider(studioProvider))
  $: loadModels(effectiveRoleProvider(reasonerProvider))

  function formatAuditTime(value) {
    if (!value) return 'unknown time'
    const d = new Date(value)
    if (Number.isNaN(d.getTime())) return value
    return d.toLocaleString()
  }

  function auditDetails(details) {
    if (!details || Object.keys(details).length === 0) return ''
    const parts = []
    if (Array.isArray(details.sections) && details.sections.length) parts.push(`sections: ${details.sections.join(', ')}`)
    if (Array.isArray(details.setting_keys) && details.setting_keys.length) parts.push(`settings: ${details.setting_keys.join(', ')}`)
    if (details.enabled_changed) parts.push('enabled changed')
    if (details.bots_changed) parts.push(`bot mappings: ${details.bots_count || 0}`)
    if (parts.length) return parts.join(' · ')
    return JSON.stringify(details)
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Configuration</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      {#if writable}
        <button class="btn-primary" on:click={save} disabled={saving || loading}>
          {saving ? 'Saving…' : 'Save changes'}
        </button>
      {/if}
    </div>
        <TourButton />
    </div>

  <div class="version-row" data-testid="version-row">
    <span class="version-label">Version</span>
    <code class="version-value">{version.version}</code>
    <span class="version-detail" class:behind={version.status === 'behind'}
          class:unknown={version.status === 'unknown'}>{version.detail}</span>
    {#if lastChecked}<span class="version-checked">{lastChecked}</span>{/if}
    <button class="btn-secondary btn-sm version-recheck"
            on:click={recheckUpdates} disabled={checkingUpdates}>
      {checkingUpdates ? 'Checking…' : 'Check for updates'}
    </button>
  </div>

  {#if updateInfo && updateInfo.update_available}
    <div class="update-banner">
      <div class="update-banner-icon">✨</div>
      <div class="update-banner-content">
        <strong>Update Available</strong>
        <span>Soulacy {updateInfo.latest_version} is available! (Current: {updateInfo.current_version}).</span>
      </div>
      <div class="update-banner-actions">
        <button class="btn-primary btn-sm" on:click={startUpgrade}>Upgrade Now</button>
        <button class="btn-secondary btn-sm" on:click={() => window.open("https://github.com/vmodekurti/soulacy-personal/releases/latest", "_blank")}>View Release Notes</button>
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

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if saved}
    <div class="banner ok restart-banner">
      <span>✓ Config saved. Run budgets apply to new runs immediately; restart the gateway for other changes to take full effect.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}
  {#if !writable && config}
    <div class="banner warn">
      ⚠ Config is read-only in this session (config file path unknown). Set <code>SOULACY_CONFIG_PATH</code> to enable writes.
    </div>
  {/if}

  {#if loading}
    <div class="empty">Loading configuration…</div>
  {:else if config}
    <div class="config-layout">
      <!-- Left: editable form -->
      <div class="form-col">

        <div class="section">
          <h2 class="section-title">LLM</h2>
          <div class="field">
            <label for="default-provider">Default provider</label>
            {#if providerOptions.length}
              <select id="default-provider" bind:value={defaultProvider} disabled={!writable}>
                {#each providerOptions as p}
                  <option value={p}>{p}</option>
                {/each}
              </select>
            {:else}
              <input id="default-provider" bind:value={defaultProvider} placeholder="ollama" disabled={!writable} />
            {/if}
          </div>

          <p class="hint">
            Task-specific model roles. Run agents on a cheap model but build &amp; reason with a
            stronger one. Leave a role blank to fall back to the default provider. A restart is
            required to take effect.
          </p>

          <div class="field-row">
            <div class="field">
              <label for="studio-provider" data-tooltip="Provider used when Studio turns natural-language requests into workflows. Leave blank to use the default provider.">Studio builder — provider</label>
              <select id="studio-provider" bind:value={studioProvider} disabled={!writable}
                      data-tooltip="Provider used when Studio turns natural-language requests into workflows. Leave blank to use the default provider."
                      on:change={() => pickProviderDefault(effectiveRoleProvider(studioProvider), (m) => studioModel = m)}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="studio-model" data-tooltip="Model Studio uses for prompt refinement, workflow generation, and repair. Stronger models usually produce better workflow graphs.">
                Studio model
                {#if modelsLoading[effectiveRoleProvider(studioProvider)]}
                  <span class="inline-status">loading…</span>
                {:else if modelsError[effectiveRoleProvider(studioProvider)]}
                  <span class="inline-error" data-tooltip={modelsError[effectiveRoleProvider(studioProvider)]}>models unavailable</span>
                {/if}
              </label>
              <select id="studio-model" bind:value={studioModel} disabled={!writable}
                      data-tooltip="Model Studio uses for prompt refinement, workflow generation, and repair. Stronger models usually produce better workflow graphs.">
                <option value="">— provider default —</option>
                {#each modelOptions(effectiveRoleProvider(studioProvider), studioModel) as m (m)}
                  <option value={m}>{m}</option>
                {/each}
                <option value="__custom__">Custom model ID…</option>
              </select>
              {#if studioModel === '__custom__'}
                <input bind:value={studioModel} placeholder="Enter exact model id" on:focus={() => studioModel = ''} disabled={!writable} />
              {/if}
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="reasoner-provider" data-tooltip="Fallback for ReAct and Plan-Execute agents that do not have a provider/model assigned. Agent-level choices always win.">Reasoner fallback — provider</label>
              <select id="reasoner-provider" bind:value={reasonerProvider} disabled={!writable}
                      data-tooltip="Fallback for ReAct and Plan-Execute agents that do not have a provider/model assigned. Agent-level choices always win."
                      on:change={() => pickProviderDefault(effectiveRoleProvider(reasonerProvider), (m) => reasonerModel = m)}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="reasoner-model" data-tooltip="Fallback model used only when the agent has no provider/model assignment.">
                Reasoner fallback model
                {#if modelsLoading[effectiveRoleProvider(reasonerProvider)]}
                  <span class="inline-status">loading…</span>
                {:else if modelsError[effectiveRoleProvider(reasonerProvider)]}
                  <span class="inline-error" data-tooltip={modelsError[effectiveRoleProvider(reasonerProvider)]}>models unavailable</span>
                {/if}
              </label>
              <select id="reasoner-model" bind:value={reasonerModel} disabled={!writable}
                      data-tooltip="Fallback model used only when the agent has no provider/model assignment.">
                <option value="">— provider default —</option>
                {#each modelOptions(effectiveRoleProvider(reasonerProvider), reasonerModel) as m (m)}
                  <option value={m}>{m}</option>
                {/each}
                <option value="__custom__">Custom model ID…</option>
              </select>
              {#if reasonerModel === '__custom__'}
                <input bind:value={reasonerModel} placeholder="Enter exact model id" on:focus={() => reasonerModel = ''} disabled={!writable} />
              {/if}
            </div>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Web search</h2>
          <p class="hint">Backs the built-in <code>web_search</code> tool. A restart is required to take effect.</p>
          <div class="field-row">
            <div class="field">
              <label for="search-provider">Provider</label>
              <select id="search-provider" bind:value={searchProvider} disabled={!writable}>
                <option value="ollama">Ollama (hosted web search)</option>
                <option value="tavily">Tavily</option>
                <option value="serper">Serper (Google)</option>
              </select>
            </div>
            <div class="field">
              <label for="search-api-key">API key</label>
              <input id="search-api-key" type="password" bind:value={searchApiKey}
                     placeholder="leave as ••• to keep current; or set env var" disabled={!writable} />
            </div>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Cost estimation</h2>
          <p class="hint">
            Optional USD-per-million-token rates used by Activity, run metrics, and <code>/api/v1/costs</code>.
            Selectors match <code>provider/model</code>, <code>provider/*</code>, then <code>*/model</code>.
            Unknown models still record tokens with <code>cost_usd: 0</code>.
          </p>
          <div class="budget-row">
            <label class="field cost-rate">
              <span data-tooltip="Maximum estimated spend allowed over a rolling 24-hour window. Set 0 to disable this budget.">Daily budget</span>
              <input type="number" step="0.01" min="0" bind:value={dailyBudgetUSD} placeholder="0.00" disabled={!writable} />
            </label>
            <label class="field cost-rate">
              <span data-tooltip="Maximum estimated spend allowed over a rolling 30-day window. Set 0 to disable this budget.">Monthly budget</span>
              <input type="number" step="0.01" min="0" bind:value={monthlyBudgetUSD} placeholder="0.00" disabled={!writable} />
            </label>
            <label class="field cost-rate">
              <span data-tooltip="Fraction of a budget that should show warning status. 0.8 means warn at 80% of budget.">Alert threshold</span>
              <input type="number" step="0.05" min="0.01" max="1" bind:value={costAlertThreshold} placeholder="0.80" disabled={!writable} />
            </label>
          </div>
          {#if costRows.length === 0}
            <p class="hint">No pricing configured yet. Add one row for exact model pricing or a provider wildcard.</p>
          {/if}
          {#each costRows as row, idx}
            <div class="cost-row">
              <label class="field cost-selector">
                <span data-tooltip="Pricing selector. Use provider/model for exact pricing, provider/* for a provider default, or */model for a shared model default.">Selector</span>
                <input bind:value={row.selector} placeholder="openai/gpt-4.1-mini or omniroute/*" disabled={!writable} />
              </label>
              <label class="field cost-rate">
                <span data-tooltip="USD charged per 1 million prompt/input tokens. Leave 0 for local or prepaid providers.">Input $/M</span>
                <input type="number" step="0.0001" min="0" bind:value={row.input} placeholder="0.00" disabled={!writable} />
              </label>
              <label class="field cost-rate">
                <span data-tooltip="USD charged per 1 million completion/output tokens. Output is often more expensive than input.">Output $/M</span>
                <input type="number" step="0.0001" min="0" bind:value={row.output} placeholder="0.00" disabled={!writable} />
              </label>
              {#if writable}
                <button class="link-danger cost-del" data-tooltip="Remove pricing row" on:click={() => removeCostRow(idx)}>✕</button>
              {/if}
            </div>
          {/each}
          {#if writable}
            <button class="btn-secondary kv-add" on:click={addCostRow}>+ Add pricing row</button>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Production SLOs</h2>
          <p class="hint">
            Launch guardrails for recent agent runs. These thresholds power Dashboard readiness and
            <code>/api/v1/runs/slo-status</code> so slow or flaky agents are visible before users depend on them.
          </p>
          <div class="slo-row">
            <label class="field">
              <span data-tooltip="Durable run-history window used for SLO checks. Examples: 24h, 7d, or 2026-07-01.">Window</span>
              <input bind:value={sloWindow} placeholder="24h" disabled={!writable} />
            </label>
            <label class="field">
              <span data-tooltip="Maximum allowed failed-run percentage in the SLO window. 0.10 means 10%.">Max failure rate</span>
              <input type="number" min="0" max="1" step="0.01" bind:value={sloMaxFailureRate} disabled={!writable} />
            </label>
            <label class="field">
              <span data-tooltip="Maximum allowed incomplete-run percentage in the SLO window. Incomplete runs usually mean timeouts, crashes, or missing final replies.">Max incomplete rate</span>
              <input type="number" min="0" max="1" step="0.01" bind:value={sloMaxIncompleteRate} disabled={!writable} />
            </label>
          </div>
          <div class="slo-row compact">
            <label class="field">
              <span data-tooltip="Maximum acceptable P95 run duration. Use Go duration syntax such as 90s, 5m, or 1h.">P95 run duration</span>
              <input bind:value={sloMaxP95RunDuration} placeholder="5m" disabled={!writable} />
            </label>
            <label class="field">
              <span data-tooltip="Minimum number of recent runs before the SLO signal is treated as reliable instead of sample-starved.">Minimum runs</span>
              <input type="number" min="1" max="1000" step="1" bind:value={sloMinRunsForSignal} disabled={!writable} />
            </label>
          </div>
          <div class="slo-row compact">
            <label class="field">
              <span data-tooltip="Channel adapter used for production alerts when budget or SLO posture reaches the selected threshold. Examples: telegram, slack, discord, webhook.">Alert channel</span>
              <input bind:value={opsAlertChannel} placeholder="telegram" disabled={!writable} />
            </label>
            <label class="field">
              <span data-tooltip="Destination for alert messages. Use a Telegram chat ID, Slack channel ID, Discord channel ID, or webhook destination depending on the channel.">Alert destination</span>
              <input bind:value={opsAlertTo} placeholder="-1001234567890" disabled={!writable} />
            </label>
            <label class="field">
              <span data-tooltip="warn sends alerts for warning and failure posture; fail sends only when a budget or SLO check is failing.">Alert threshold</span>
              <select bind:value={opsAlertMinStatus} disabled={!writable}>
                <option value="fail">fail only</option>
                <option value="warn">warn or fail</option>
              </select>
            </label>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Deployment profile</h2>
          <p class="hint">
            Controls how strict launch readiness should be. Production treats missing auth, providers,
            enabled agents, outbound delivery, update manifest, cost guardrails, and run SLOs as launch blockers.
          </p>
          <div class="field-row">
            <div class="field">
              <label for="deployment-profile" data-tooltip="Select local, development, staging, or production. Production enables strict launch blockers; local/development keep checks advisory.">Profile</label>
              <select id="deployment-profile" bind:value={deploymentProfile} disabled={!writable}
                      data-tooltip="Select local, development, staging, or production. Production enables strict launch blockers; local/development keep checks advisory.">
                <option value="local">local</option>
                <option value="development">development</option>
                <option value="staging">staging</option>
                <option value="production">production</option>
              </select>
            </div>
            <div class="field">
              <label for="deployment-owner" data-tooltip="Team or person accountable for this workspace in production readiness reports.">Owner</label>
              <input id="deployment-owner" bind:value={deploymentOwner} placeholder="platform-team" disabled={!writable}
                     data-tooltip="Team or person accountable for this workspace in production readiness reports." />
            </div>
            <div class="field">
              <label for="deployment-region" data-tooltip="Primary region, environment, or customer location for this workspace.">Region</label>
              <input id="deployment-region" bind:value={deploymentRegion} placeholder="us-central" disabled={!writable}
                     data-tooltip="Primary region, environment, or customer location for this workspace." />
            </div>
          </div>
          <div class="field">
            <label for="deployment-notes" data-tooltip="Optional context shown in config and deployment diagnostics.">Notes</label>
            <textarea id="deployment-notes" bind:value={deploymentNotes} rows="2"
                      placeholder="Customer-facing workspace, staging burn-in, local demo, etc."
                      disabled={!writable}
                      data-tooltip="Optional context shown in config and deployment diagnostics."></textarea>
          </div>
        </div>

        <!-- F-GUI-6 — Security section: workspace-default intent-gate mode + explainer.
             The intent gate (S3) sits after the prompt-injection scanner (S2) and
             before the runtime executes any tool call; its mode governs how the
             gate reacts when a risky call is not clearly justified by the user's
             stated goal. -->
        <div class="section">
          <h2 class="section-title">Security</h2>
          <p class="hint">
            The <strong>intent gate</strong> is the last check before a risky tool call runs.
            It reasons about the user's stated goal, the last untrusted evidence source, and any
            prompt-injection findings the scanner flagged. This picks your workspace-default policy;
            individual agents can still override via <code>security.intent_gate</code> in their SOUL.yaml.
          </p>
          <div class="field">
            <label for="intent-gate-mode">Intent gate mode</label>
            <div class="intent-gate-radio">
              <label class:on={securityIntentGate === 'off'} data-tooltip="Warning: Disables all injection safeguards on tool calls">
                <input type="radio" name="intent-gate-mode" value="off"
                       bind:group={securityIntentGate} disabled={!writable} />
                <div>
                  <strong>Off</strong>
                  <span>Bypass the gate. Advanced operators only — no guardrails against
                  injection-driven tool escalation.</span>
                </div>
              </label>
              <label class:on={!securityIntentGate || securityIntentGate === '' || securityIntentGate === 'prompt'} data-tooltip="Standard security confirmation prompt for high-risk calls">
                <input type="radio" name="intent-gate-mode" value="prompt"
                       bind:group={securityIntentGate} disabled={!writable} />
                <div>
                  <strong>Prompt <span class="pill">default</span></strong>
                  <span>Confirm risky tool calls when a High-severity injection pattern was
                  detected on the last untrusted source. Runs the confirmation flow through the
                  bound channel.</span>
                </div>
              </label>
              <label class:on={securityIntentGate === 'deny'} data-tooltip="Recommended: Rejects any risky tool call under active injection">
                <input type="radio" name="intent-gate-mode" value="deny"
                       bind:group={securityIntentGate} disabled={!writable} />
                <div>
                  <strong>Deny <span class="pill pill-prod">recommended for production</span></strong>
                  <span>Hard-deny risky tool calls under any injection-influenced context, and
                  prompt on Medium-severity findings. Matches S4 production readiness expectations.</span>
                </div>
              </label>
            </div>
          </div>
          <p class="hint">
            To apply the mode across every agent, add
            <code>security:</code>
            <code>&nbsp;&nbsp;intent_gate: {securityIntentGate || 'prompt'}</code>
            to each SOUL.yaml (or open <a href="#agents">Agents</a> and use the YAML editor).
            Cohort F backend enforcement reads the per-agent value; this workspace default is a
            future backend hook and a policy record.
          </p>
        </div>

        <div class="section">
          <h2 class="section-title">Runtime</h2>
          <div class="field-row">
            <div class="field">
              <label for="python-bin">Python binary</label>
              <input id="python-bin" bind:value={pythonBin} placeholder="python3" disabled={!writable} />
            </div>
            <div class="field">
              <label for="tool-timeout">Tool timeout</label>
              <input id="tool-timeout" bind:value={toolTimeout} placeholder="30s" disabled={!writable} />
            </div>
          </div>
          <div class="field-row">
            <div class="field">
              <label for="max-turns">Default max turns</label>
              <input id="max-turns" type="number" bind:value={maxTurns} min="1" max="100" disabled={!writable} />
            </div>
            <div class="field">
              <label for="max-sessions">Max concurrent sessions</label>
              <input id="max-sessions" type="number" bind:value={maxSessions} min="1" max="1000" disabled={!writable} />
            </div>
            <div class="field">
              <label for="max-agent-call-depth" data-tooltip="Caps recursive peer-agent delegation chains. Raise for deeper coordinator teams; lower to stop accidental loops sooner. Default is 5.">Max agent-call depth</label>
              <input id="max-agent-call-depth" type="number" bind:value={maxAgentCallDepth} min="1" max="50" disabled={!writable} />
            </div>
          </div>
          <h3>Agent run budgets</h3>
          <p class="hint">
            These limits cover an entire run, not one response. Input history and tool schemas are
            counted again on each model call, so tool-heavy agents need substantially more than their
            visible answer length. Set a value to <strong>0</strong> for unlimited execution; usage and
            estimated cost reporting continue even when enforcement is unlimited. Changes apply to
            newly started runs immediately — no restart needed.
          </p>
          <div class="budget-row">
            <label class="field cost-rate">
              <span data-tooltip="Default total input + output tokens available to an agent run. 0 means unlimited.">Default run tokens</span>
              <input type="number" min="0" step="10000" bind:value={defaultBudgetTokens} disabled={!writable} />
            </label>
            <label class="field cost-rate">
              <span data-tooltip="Default number of model calls available to one run. Tool calls often require another model call. 0 means unlimited.">Default model calls</span>
              <input type="number" min="0" step="1" bind:value={defaultBudgetCalls} disabled={!writable} />
            </label>
            <label class="field cost-rate">
              <span data-tooltip="Highest token budget an individual agent may request. 0 removes the workspace ceiling.">Per-agent token ceiling</span>
              <input type="number" min="0" step="10000" bind:value={maxBudgetTokens} disabled={!writable} />
            </label>
            <label class="field cost-rate">
              <span data-tooltip="Highest model-call budget an individual agent may request. 0 removes the workspace ceiling.">Per-agent call ceiling</span>
              <input type="number" min="0" step="1" bind:value={maxBudgetCalls} disabled={!writable} />
            </label>
          </div>
        </div>

        <div class="section">
          <div class="section-heading">
            <h2 class="section-title">Executors</h2>
            <button class="btn-secondary tiny-btn" on:click={loadExecutors} disabled={executorLoading}>
              {executorLoading ? 'Checking…' : 'Re-check'}
            </button>
          </div>
          <p class="hint">
            Choose where Python tools run. Local process is simplest, pool is faster for chat,
            Docker isolates code, SSH/cloud move heavy work off this machine. Restart after changes.
          </p>
          {#if executorError}
            <div class="mini-warn">{executorError}</div>
          {/if}
          {#if executorReadiness}
            <div class="executor-grid">
              {#each executorReadiness.backends || [] as backend (backend.key)}
                <div class:active={backend.active} class={`executor-card ${backend.status}`}>
                  <div class="executor-top">
                    <strong>{backend.label}</strong>
                    <span>{backend.status}</span>
                  </div>
                  <p>{backend.detail}</p>
                  {#if backend.next}<small>{backend.next}</small>{/if}
                  {#if backend.command}<code>{backend.command}</code>{/if}
                </div>
              {/each}
            </div>
          {/if}

          <div class="field-row">
            <div class="field">
              <label for="executor-backend" data-tooltip="Default backend for Python tools. Agents can override it per tool with execution.backend when needed.">Default backend</label>
              <select id="executor-backend" bind:value={executorBackend} disabled={!writable}>
                <option value="process">process — fresh local process</option>
                <option value="pool">pool — warm local workers</option>
                <option value="docker">docker — isolated container</option>
                <option value="ssh">ssh — remote worker</option>
              </select>
            </div>
            <div class="field">
              <label for="executor-workers" data-tooltip="Number of warm local Python workers used by pool mode. Higher values improve concurrency but consume memory.">Pool workers</label>
              <input id="executor-workers" type="number" bind:value={executorWorkers} min="1" max="64" disabled={!writable} />
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="docker-image" data-tooltip="Container image used by docker execution. Keep it pinned for production repeatability.">Docker image</label>
              <input id="docker-image" bind:value={dockerImage} placeholder="python:3.12-slim" disabled={!writable} />
            </div>
            <div class="field">
              <label for="docker-network" data-tooltip="Docker network mode. Use none for safer sandboxing, bridge only when tools need outbound network access.">Docker network</label>
              <input id="docker-network" bind:value={dockerNetwork} placeholder="none" disabled={!writable} />
            </div>
          </div>
          <div class="field">
            <label for="docker-volumes" data-tooltip="Explicit Docker volume allowlist, one mount per line as host:container[:ro]. Empty means no host paths are mounted.">Docker volume allowlist</label>
            <textarea id="docker-volumes" bind:value={dockerVolumes} rows="2" placeholder="/safe/data:/data:ro" disabled={!writable}></textarea>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="ssh-host" data-tooltip="Remote host for SSH execution. Use host or user@host.">SSH host</label>
              <input id="ssh-host" bind:value={sshHost} placeholder="worker.example.com" disabled={!writable} />
            </div>
            <div class="field">
              <label for="ssh-user" data-tooltip="Optional SSH username when it is not included in SSH host.">SSH user</label>
              <input id="ssh-user" bind:value={sshUser} placeholder="ubuntu" disabled={!writable} />
            </div>
          </div>
          <div class="field-row">
            <div class="field">
              <label for="ssh-python-bin" data-tooltip="Python executable on the remote host.">SSH Python binary</label>
              <input id="ssh-python-bin" bind:value={sshPythonBin} placeholder="python3" disabled={!writable} />
            </div>
            <div class="field">
              <label for="ssh-identity-credential" data-tooltip="Name of a vault secret containing the SSH private key. Prefer this over raw key paths.">SSH identity credential</label>
              <input id="ssh-identity-credential" bind:value={sshIdentityCredential} placeholder="remote-worker-key" disabled={!writable} />
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="cloud-preset" data-tooltip="Optional cloud execution preset. The provider CLI must already be installed and authenticated on this host.">Cloud preset</label>
              <select id="cloud-preset" bind:value={cloudPreset} disabled={!writable}>
                <option value="">— none —</option>
                <option value="modal">modal</option>
                <option value="runpod">runpod</option>
                <option value="daytona">daytona</option>
              </select>
            </div>
            <div class="field">
              <label for="cloud-target" data-tooltip="Provider-specific cloud target such as a workspace, app, image, or pod id.">Cloud target</label>
              <input id="cloud-target" bind:value={cloudTarget} placeholder="workspace/app/pod id" disabled={!writable} />
            </div>
          </div>
          <div class="field">
            <label for="cloud-cli" data-tooltip="Optional CLI binary override for the selected cloud preset. Leave blank to use the default CLI name.">Cloud CLI override</label>
            <input id="cloud-cli" bind:value={cloudCLI} placeholder="modal, runpodctl, or daytona" disabled={!writable} />
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Logging</h2>
          <div class="field-row">
            <div class="field">
              <label for="log-level" data-tooltip="Set details level for gateway execution logs">Level</label>
              <select id="log-level" bind:value={logLevel} disabled={!writable}>
                <option value="debug">debug</option>
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </div>
            <div class="field">
              <label for="log-format">Format</label>
              <select id="log-format" bind:value={logFormat} disabled={!writable}>
                <option value="console">console</option>
                <option value="json">json</option>
              </select>
            </div>
          </div>
          <div class="field">
            <label for="log-file">Log file path (empty = stdout only)</label>
            <input id="log-file" bind:value={logFile} placeholder="/var/log/soulacy.log" disabled={!writable} />
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Webhooks</h2>
          {#if (config?.hooks || []).length === 0}
            <p class="hint">
              No outbound webhooks configured. Add a <code>hooks:</code> section
              to <code>config.yaml</code> to deliver signed events
              (run.failed, tool.call, …) to your own endpoints — see
              <code>docs/EVENTS.md</code> for the payload schema and signature
              verification.
            </p>
          {:else}
            {#each config.hooks as h}
              <div class="hook-row">
                <code class="hook-url">{h.url}</code>
                <span class="hook-meta">
                  on: {(h.on || []).join(', ') || '—'}
                  {#if (h.agents || []).length}· agents: {h.agents.join(', ')}{/if}
                  {#if h.secret_env}· signed (secret from ${h.secret_env}){:else}· ⚠ unsigned{/if}
                </span>
              </div>
            {/each}
            <p class="hint">Edit webhooks in <code>config.yaml</code> (restart to apply).</p>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Plugin settings</h2>
          {#if Object.keys(pluginRows).length === 0}
            <p class="hint">
              No plugin settings configured. Add a plugin ID below (or a
              <code>plugins_config:</code> block in <code>config.yaml</code>) —
              each plugin documents its own keys. Secret-looking values are
              redacted here and never reach the browser.
            </p>
          {:else}
            {#each Object.entries(pluginRows) as [pid, rows] (pid)}
              <div class="plugin-settings">
                <code class="hook-url">{pid}</code>
                {#each rows as row, idx}
                  <div class="kv-row">
                    <input type="text" class="kv-key" placeholder="key"
                           bind:value={row.key} disabled={!writable} />
                    <input type="text" class="kv-val" placeholder="value (JSON for objects/numbers)"
                           bind:value={row.value} disabled={!writable} />
                    {#if writable}
                      <button class="link-danger kv-del" data-tooltip="Remove key"
                              on:click={() => removeRow(pid, idx)}>✕</button>
                    {/if}
                  </div>
                {/each}
                {#if writable}
                  <button class="btn-secondary kv-add" on:click={() => addRow(pid)}>+ Add key</button>
                {/if}
              </div>
            {/each}
            <p class="hint">
              Secrets show as <code>***</code> — leaving them unchanged keeps
              the real value on disk. Removing a row deletes that key on save.
            </p>
          {/if}
          {#if writable}
            <div class="kv-row">
              <input type="text" class="kv-key" placeholder="plugin id"
                     bind:value={newPluginId} />
              <button class="btn-secondary" on:click={addPlugin}
                      disabled={!newPluginId.trim()}>+ Add plugin section</button>
            </div>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Support</h2>
          <p class="hint">
            Download a redacted diagnostic bundle with doctor output, launch readiness,
            unified run ledger, admin audit, masked configuration, agent manifests, and recent log tails.
          </p>
          <button class="btn-secondary support-download" on:click={downloadSupportBundle} disabled={downloadingSupport}>
            {downloadingSupport ? 'Preparing bundle...' : 'Download support bundle'}
          </button>
        </div>

        <div class="section">
          <div class="section-heading">
            <h2 class="section-title">Admin audit</h2>
            <button class="btn-secondary tiny-btn" on:click={loadAdminAudit} disabled={auditLoading}
                    data-tooltip="Refresh the recent config, channel, secret, and gateway restart mutations recorded in the durable action log.">
              {auditLoading ? 'Loading…' : 'Refresh'}
            </button>
          </div>
          <p class="hint">
            Recent configuration mutations recorded in the durable action log. Secret values are never stored here.
          </p>
          {#if auditError}
            <div class="mini-warn">{auditError}</div>
          {:else if adminAudit?.events?.length}
            <div class="audit-list">
              {#each adminAudit.events as ev}
                <div class="audit-row">
                  <div class="audit-main">
                    <strong>{ev.action}</strong>
                    <span>{ev.resource}{ev.target ? ` · ${ev.target}` : ''}</span>
                  </div>
                  <div class="audit-meta">
                    <span>{formatAuditTime(ev.timestamp)}</span>
                    <span>{ev.actor || 'unknown actor'}</span>
                    <span class={`audit-status ${ev.status || 'ok'}`}>{ev.status || 'ok'}</span>
                  </div>
                  {#if auditDetails(ev.details)}
                    <small>{auditDetails(ev.details)}</small>
                  {/if}
                </div>
              {/each}
            </div>
          {:else}
            <p class="hint">No admin changes recorded yet.</p>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Directories</h2>
          <div class="field">
            <label for="agent-dirs">Agent directories (one per line)</label>
            <textarea id="agent-dirs" bind:value={agentDirs} rows="3"
                      placeholder="~/.soulacy/agents" disabled={!writable}></textarea>
          </div>
          <div class="field">
            <label for="skill-dirs">Extra skill directories (one per line)</label>
            <textarea id="skill-dirs" bind:value={skillDirs} rows="2"
                      placeholder="~/.soulacy/skills" disabled={!writable}></textarea>
          </div>
        </div>
      </div>

      <!-- Right: read-only full JSON view -->
      <div class="json-col">
        <div class="json-header">
          <span class="json-label">Current config (read-only view)</span>
          {#if config._meta?.config_path}
            <code class="json-path">{config._meta.config_path}</code>
          {/if}
        </div>
        <pre class="json-view">{JSON.stringify(config, null, 2)}</pre>
      </div>
    </div>
  {/if}
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok   { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn { background: rgba(240,160,96,.1); border: 1px solid rgba(240,160,96,.3); color: #f0a060; }
  .warn code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; }
  .empty { padding: 3rem; text-align: center; color: #6b7294; }

  .config-layout { display: grid; grid-template-columns: 1fr 380px; gap: 1.25rem; align-items: start; }

  @media (max-width: 900px) {
    .config-layout { grid-template-columns: 1fr; }
    .json-col { position: static; }
  }
  @media (max-width: 640px) {
    .field-row { grid-template-columns: 1fr; }
  }

  /* Form column */
  .form-col { display: flex; flex-direction: column; gap: 1rem; }

  .section { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .85rem; }
  .section-heading { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .section-title { font-size: .78rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; color: #555a7a; margin-bottom: .1rem; }

  .field { display: flex; flex-direction: column; gap: .35rem; }
  .field label { font-size: .78rem; color: #7b82a8; font-weight: 500; }
  .field-row { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .inline-status { margin-left: .35rem; color: #8b85ff; font-size: .72rem; font-weight: 500; }
  .inline-error { margin-left: .35rem; color: #f0a060; font-size: .72rem; font-weight: 500; cursor: help; }
  .tiny-btn { font-size: .72rem; padding: .25rem .55rem; }
  .mini-warn { padding: .5rem .65rem; border-radius: 8px; background: rgba(240,160,96,.1); border: 1px solid rgba(240,160,96,.25); color: #f0a060; font-size: .75rem; }
  .executor-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: .55rem; }
  .executor-card { padding: .65rem; border: 1px solid #202542; border-radius: 8px; background: #10121f; display: flex; flex-direction: column; gap: .4rem; min-height: 132px; }
  .executor-card.active { border-color: #7567ff; box-shadow: 0 0 0 1px rgba(117,103,255,.25); }
  .executor-card.ok { border-color: rgba(76,175,130,.32); }
  .executor-card.warn { border-color: rgba(240,160,96,.35); }
  .executor-card.fail { border-color: rgba(240,96,96,.4); }
  .executor-top { display: flex; align-items: center; justify-content: space-between; gap: .5rem; }
  .executor-top strong { font-size: .78rem; color: #c5c8e8; }
  .executor-top span { font-size: .65rem; text-transform: uppercase; letter-spacing: .05em; color: #8b85ff; }
  .executor-card p { margin: 0; color: #9aa1c6; font-size: .74rem; line-height: 1.45; }
  .executor-card small { color: #6b7294; font-size: .7rem; line-height: 1.35; }
  .executor-card code { margin-top: auto; background: #0b0d19; border: 1px solid #1a1e36; padding: .35rem; border-radius: 6px; color: #8b85ff; font-size: .68rem; overflow-wrap: anywhere; }
  .audit-list { display: flex; flex-direction: column; gap: .5rem; }
  .audit-row { border: 1px solid #202542; border-radius: 8px; background: #10121f; padding: .65rem .75rem; display: flex; flex-direction: column; gap: .35rem; }
  .audit-main { display: flex; align-items: baseline; justify-content: space-between; gap: .75rem; }
  .audit-main strong { color: #e5e7ff; font-size: .82rem; }
  .audit-main span { color: #7b82a8; font-size: .75rem; text-align: right; }
  .audit-meta { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; color: #68709a; font-size: .72rem; }
  .audit-status { padding: .08rem .35rem; border-radius: 999px; background: rgba(76,175,130,.12); color: #4caf82; }
  .audit-status.accepted { background: rgba(139,133,255,.14); color: #aaa5ff; }
  .audit-row small { color: #8a91b8; font-size: .72rem; line-height: 1.35; }

  /* JSON column */
  .json-col {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    overflow: hidden; position: sticky; top: 1.5rem;
  }
  .json-header {
    padding: .65rem 1rem; border-bottom: 1px solid #1a1e36;
    display: flex; flex-direction: column; gap: .2rem;
  }
  .json-label { font-size: .72rem; color: #555a7a; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; }
  .json-path  { font-size: .72rem; color: #7b82a8; word-break: break-all; }
  .json-view  {
    font-family: monospace; font-size: .75rem; line-height: 1.6;
    color: #7b82a8; padding: 1rem; overflow-x: auto;
    white-space: pre; max-height: 600px; overflow-y: auto;
  }

  .hint { font-size: .78rem; color: #6b7294; line-height: 1.6; }
  .hint code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; font-size: .72rem; }
  .hook-row { display: flex; flex-direction: column; gap: .15rem; padding: .5rem .65rem;
              background: #10121f; border: 1px solid #1a1e36; border-radius: 8px; }
  .hook-url { font-size: .78rem; color: #8b85ff; overflow-wrap: anywhere; }
  .hook-meta { font-size: .7rem; font-family: monospace; color: #6b7294; }
  .plugin-settings { display: flex; flex-direction: column; gap: .35rem; padding: .5rem .65rem;
                     background: #10121f; border: 1px solid #1a1e36; border-radius: 8px;
                     margin-bottom: .5rem; }
  .support-download { align-self: flex-start; }
  .kv-row { display: flex; gap: .4rem; align-items: center; }
  .kv-key { flex: 0 0 32%; }
  .kv-val { flex: 1; font-family: monospace; font-size: .78rem; }
  .kv-del { flex: 0 0 auto; }
  .kv-add { align-self: flex-start; font-size: .75rem; padding: .25rem .6rem; }
  .cost-row {
    display: grid;
    grid-template-columns: minmax(180px, 1fr) 110px 110px auto;
    gap: .5rem;
    align-items: end;
    padding: .55rem .65rem;
    background: #10121f;
    border: 1px solid #1a1e36;
    border-radius: 8px;
  }
  .cost-selector input { font-family: monospace; font-size: .78rem; }
  .cost-rate input { text-align: right; }
  .cost-del { align-self: end; margin-bottom: .15rem; }
  .budget-row {
    display: grid;
    grid-template-columns: repeat(3, minmax(120px, 180px));
    gap: .6rem;
    align-items: end;
    margin: .65rem 0 .85rem;
  }
  .slo-row {
    display: grid;
    grid-template-columns: repeat(3, minmax(130px, 1fr));
    gap: .6rem;
    align-items: end;
    margin: .65rem 0 .85rem;
  }
  .slo-row.compact {
    grid-template-columns: repeat(2, minmax(130px, 220px));
  }
  @media (max-width: 640px) {
    .cost-row { grid-template-columns: 1fr; }
    .budget-row { grid-template-columns: 1fr; }
    .slo-row, .slo-row.compact { grid-template-columns: 1fr; }
    .cost-rate input { text-align: left; }
  }

  /* F-GUI-6 — intent-gate mode radio. Three vertically stacked cards so each
     mode has room for its one-line explainer alongside the option name. */
  .intent-gate-radio {
    display: flex; flex-direction: column; gap: .45rem;
    margin-top: .35rem;
  }
  .intent-gate-radio > label {
    display: grid; grid-template-columns: auto 1fr;
    align-items: flex-start; gap: .55rem;
    padding: .55rem .7rem;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    color: #c8cadf; cursor: pointer;
    transition: border-color .15s ease;
  }
  .intent-gate-radio > label:hover { border-color: #2a2f4a; }
  .intent-gate-radio > label.on {
    background: rgba(108,99,255,.08); border-color: rgba(108,99,255,.4);
  }
  .intent-gate-radio input[type=radio] { margin-top: .25rem; }
  .intent-gate-radio strong { color: #e7e8f5; font-size: .84rem; display: block; margin-bottom: .15rem; }
  .intent-gate-radio span { color: #7b82a8; font-size: .76rem; line-height: 1.5; display: block; }
  .intent-gate-radio .pill {
    display: inline-block; margin-left: .35rem;
    padding: .05rem .4rem; border-radius: 999px;
    font-size: .62rem; font-weight: 700; letter-spacing: .04em; text-transform: uppercase;
    background: rgba(139,220,255,.15); color: #8bdcff;
  }
  .intent-gate-radio .pill.pill-prod {
    background: rgba(240,96,96,.18); color: #f06060;
  }
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
  .version-row {
    display: flex;
    align-items: center;
    gap: .6rem;
    flex-wrap: wrap;
    padding: .55rem .9rem;
    border: 1px solid rgba(255,255,255,.08);
    border-radius: 8px;
    font-size: .85rem;
  }
  .version-label { opacity: .65; }
  .version-value { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .version-detail { opacity: .75; }
  .version-detail.behind { color: #e0b341; opacity: 1; }
  .version-detail.unknown { color: #f06060; opacity: 1; }
  .version-checked { opacity: .45; font-size: .78rem; }
  .version-recheck { margin-left: auto; }
</style>
