<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let providers       = {}
  let defaultProvider = ''
  let known           = []   // known provider ids the GUI offers
  let registered      = []   // currently-registered provider ids in the live router
  let models          = {}   // provider → model list
  let chosen          = {}   // provider → selected model (for Save)
  let modelsLoading   = {}   // provider → bool
  let savingModel     = {}   // provider → bool
  let loading         = true
  let testResults     = {}   // provider → {ok, msg}
  let error           = ''
  let notice          = ''
  let restartNeeded   = false
  let restarting      = false
  let doctor          = []
  let vaultCheck      = null
  let doctorLoading   = false

  // Add/edit-credentials modal state
  let showAdd  = false
  let addId    = 'openai'
  let addKey   = ''
  let addBase  = ''
  let addModel = ''
  let addKeepAlive = ''
  let addPromptCaching = false
  // Google
  let addThinkingBudget = 0
  let addSafetyLevel = ''
  // Anthropic
  let addExtendedThinking = false
  let addAnthropicThinkingBudget = 8192
  // OpenAI / compatible
  let addOrganization = ''
  let addParallelToolCalls = null  // null=default, true, false
  // Ollama
  let ollamaOptions = {}
  let ollamaOptionsJSON = ''
  let saving   = false
  let customId = ''

  const KNOWN_BASES = {
    openai:      'https://api.openai.com/v1',
    anthropic:   'https://api.anthropic.com',
    google:      'https://generativelanguage.googleapis.com',
    ollama:      'http://localhost:11434',
    groq:        'https://api.groq.com/openai/v1',
    mistral:     'https://api.mistral.ai/v1',
    openrouter:  'https://openrouter.ai/api/v1',
    deepseek:    'https://api.deepseek.com',
    together:    'https://api.together.xyz/v1',
    grok:        'https://api.x.ai/v1',
  }
  const KNOWN_MODELS = {
    openai:      'gpt-4o-mini',
    anthropic:   'claude-3-5-sonnet-latest',
    google:      'gemini-2.5-flash',
    ollama:      'llama3',
    groq:        'llama-3.3-70b-versatile',
    mistral:     'mistral-small-latest',
    openrouter:  'meta-llama/llama-3.3-70b-instruct:free',
    deepseek:    'deepseek-chat',
    together:    'meta-llama/Llama-3-70b-chat-hf',
    grok:        'grok-2-1212',
  }
  const DEFAULT_OLLAMA_KEEP_ALIVE = '30m'
  const DEFAULT_OLLAMA_OPTIONS = { num_ctx: 4096, num_batch: 128 }
  const ADVANCED_OPTIONS_PLACEHOLDER = '{"top_k": 40, "repeat_penalty": 1.1}'
  const OLLAMA_OPTION_FIELDS = [
    { key: 'num_ctx', label: 'Context window', type: 'number', placeholder: '4096', help: 'More tokens of conversation/context. Higher values use more memory and slow prompt evaluation.' },
    { key: 'num_batch', label: 'Prompt batch size', type: 'number', placeholder: '128', help: 'Can speed prompt ingestion when memory allows. Reduce if large prompts cause memory pressure.' },
    { key: 'num_gpu', label: 'GPU layers', type: 'number', placeholder: '-1', help: 'How many layers to offload. Usually leave blank on Apple Silicon unless Ollama under-utilizes GPU.' },
    { key: 'main_gpu', label: 'Main GPU', type: 'number', placeholder: '0', help: 'Only useful on multi-GPU machines.' },
    { key: 'num_thread', label: 'CPU threads', type: 'number', placeholder: '', help: 'CPU inference thread count. Leave blank on Apple Silicon unless benchmarking.' },
    { key: 'use_mmap', label: 'Memory-map model', type: 'boolean', help: 'Usually true/default. Can reduce load overhead.' },
    { key: 'numa', label: 'NUMA mode', type: 'boolean', help: 'Linux multi-socket tuning. Usually off on desktops and Macs.' },
  ]

  async function load() {
    loading = true
    error   = ''
    try {
      const res       = await api.providers.list()
      providers       = res.providers || {}
      defaultProvider = res.default_provider || ''
      known           = res.known || []
      registered      = res.registered || []
      await runDoctor()
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function runDoctor() {
    doctorLoading = true
    try {
      const res = await api.providers.doctor()
      doctor = res.providers || []
      vaultCheck = res.vault || null
    } catch (e) {
      error = e.message
    } finally {
      doctorLoading = false
    }
  }

  function openAdd(id) {
    addId    = id || 'openai'
    customId = (addId && !known.includes(addId)) ? addId : ''
    addKey   = ''
    addBase  = providers[addId]?.base_url || KNOWN_BASES[addId] || ''
    addModel = providers[addId]?.model    || KNOWN_MODELS[addId] || ''
    addKeepAlive = addId === 'ollama'
      ? (providers[addId]?.keep_alive || DEFAULT_OLLAMA_KEEP_ALIVE)
      : ''
    addPromptCaching = providers[addId]?.prompt_caching ?? false
    // Google
    addThinkingBudget = providers[addId]?.thinking_budget ?? 0
    addSafetyLevel = providers[addId]?.safety_level ?? ''
    // Anthropic
    addExtendedThinking = providers[addId]?.extended_thinking ?? false
    addAnthropicThinkingBudget = providers[addId]?.thinking_budget ?? 8192
    // OpenAI / compatible
    addOrganization = providers[addId]?.organization ?? ''
    addParallelToolCalls = providers[addId]?.parallel_tool_calls ?? null
    // Ollama
    ollamaOptions = addId === 'ollama'
      ? { ...DEFAULT_OLLAMA_OPTIONS, ...(providers[addId]?.options || {}) }
      : {}
    ollamaOptionsJSON = ''
    showAdd  = true
  }
  async function saveCreds() {
    if (!addId) return
    saving = true; error = ''; notice = ''
    try {
      let targetId = addId
      if (addId === 'custom') {
        if (!customId.trim()) {
          throw new Error('Custom Provider ID is required')
        }
        targetId = customId.trim().toLowerCase()
        if (!/^[a-z0-9-_]+$/.test(targetId)) {
          throw new Error('Provider ID must be lowercase alphanumeric characters, dashes, or underscores.')
        }
        if (known.includes(targetId)) {
          throw new Error(`"${targetId}" is a built-in provider. Please select it from the Provider dropdown list instead.`)
        }
      }
      const body = {
        base_url: addBase || undefined,
        api_key:  addKey  || undefined,
        model:    addModel || undefined,
      }
      if (targetId === 'ollama') {
        body.keep_alive = addKeepAlive
        body.options = buildOllamaOptions()
      }
      body.prompt_caching = addPromptCaching
      if (targetId === 'google') {
        body.thinking_budget = addThinkingBudget
        body.safety_level = addSafetyLevel
      }
      if (targetId === 'anthropic') {
        body.extended_thinking = addExtendedThinking
        if (addExtendedThinking) body.thinking_budget = addAnthropicThinkingBudget
      }
      if (targetId !== 'ollama' && targetId !== 'google' && targetId !== 'anthropic') {
        if (addOrganization) body.organization = addOrganization
        if (addParallelToolCalls !== null) body.parallel_tool_calls = addParallelToolCalls
      }
      const res = await api.providers.setCredentials(targetId, body)
      restartNeeded = false
      showAdd = false
      await load()
      // Save-first, then list: if the provider is now live in the router, pull
      // its real models right away so the user can pick one without a separate
      // click. If it needs a restart to register, skip the lookup (it would
      // fail) and just confirm the save.
      if ((registered || []).includes(targetId)) {
        notice = `${res.message || 'Saved.'} Select a model below to finish.`
        await listModels(targetId)
      } else {
        notice = res.message || 'Saved.'
      }
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }
  function updateOllamaOption(key, raw, type = 'number') {
    const next = { ...ollamaOptions }
    if (raw === '' || raw == null) {
      delete next[key]
    } else if (type === 'boolean') {
      next[key] = raw === 'true'
    } else {
      const n = Number(raw)
      if (!Number.isNaN(n)) next[key] = n
    }
    ollamaOptions = next
  }
  function buildOllamaOptions() {
    let out = { ...ollamaOptions }
    if (ollamaOptionsJSON.trim()) {
      const extra = JSON.parse(ollamaOptionsJSON)
      if (!extra || typeof extra !== 'object' || Array.isArray(extra)) {
        throw new Error('Ollama advanced options must be a JSON object.')
      }
      out = { ...out, ...extra }
    }
    for (const [k, v] of Object.entries(out)) {
      if (v === '' || v == null) delete out[k]
    }
    return out
  }
  function applyOllamaPreset(name) {
    if (name === 'balanced') {
      addKeepAlive = DEFAULT_OLLAMA_KEEP_ALIVE
      ollamaOptions = { ...DEFAULT_OLLAMA_OPTIONS }
    } else if (name === 'long') {
      addKeepAlive = '30m'
      ollamaOptions = { num_ctx: 8192, num_batch: 128 }
    } else if (name === 'resident') {
      addKeepAlive = '-1'
      ollamaOptions = { num_ctx: 4096, num_batch: 128 }
    } else if (name === 'clear') {
      addKeepAlive = DEFAULT_OLLAMA_KEEP_ALIVE
      ollamaOptions = { ...DEFAULT_OLLAMA_OPTIONS }
      ollamaOptionsJSON = ''
    }
  }
  $: missingKnown = (known || []).filter(id => !(id in providers))

  async function listModels(providerId) {
    modelsLoading = { ...modelsLoading, [providerId]: true }
    error = ''
    try {
      const res = await api.providers.models(providerId)
      models = { ...models, [providerId]: res.models || [] }
      // Default the selection to the currently-configured model, else the first one.
      const current = res.selected || providers[providerId]?.model || ''
      chosen = { ...chosen, [providerId]: current || (res.models && res.models[0]) || '' }
    } catch (e) {
      models = { ...models, [providerId]: [] }
      error = `${providerId}: ${e.message}`
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  async function saveModel(providerId) {
    const model = chosen[providerId]
    if (!model) return
    savingModel = { ...savingModel, [providerId]: true }
    error = ''; notice = ''
    try {
      const res = await api.providers.setModel(providerId, model)
      // reflect immediately
      providers = { ...providers, [providerId]: { ...providers[providerId], model } }
      notice = res.message || `Saved ${model} as the default model for ${providerId}.`
      restartNeeded = false
    } catch (e) {
      error = e.message
    } finally {
      savingModel = { ...savingModel, [providerId]: false }
    }
  }

  async function test(providerId) {
    testResults = { ...testResults, [providerId]: { loading: true } }
    try {
      await api.providers.models(providerId)
      testResults = { ...testResults, [providerId]: { ok: true, msg: 'Reachable ✓' } }
    } catch (e) {
      // E4 — Failure handling: the backend now attaches a diagnosis
      // ({category, reason, fix, detail}) to the error body so we can show a
      // plain-English reason and a concrete fix instead of the raw
      // "openai: /models returned 401: {...}" wire error. Older responses that
      // pre-date the diagnosis field still fall back to e.message.
      const d = e?.body?.diagnosis || null
      testResults = {
        ...testResults,
        [providerId]: {
          ok: false,
          msg: d?.reason || e.message,
          fix: d?.fix || '',
          category: d?.category || '',
          detail: d?.detail || e.body?.detail || '',
          showDetail: false,
        },
      }
    }
  }

  function toggleTestDetail(providerId) {
    const r = testResults[providerId]
    if (!r) return
    testResults = { ...testResults, [providerId]: { ...r, showDetail: !r.showDetail } }
  }

  async function restartGateway() {
    restarting = true
    error = ''
    try {
      await api.admin.restart()
      restartNeeded = false
      notice = 'Restart requested. Reconnect this page in a few seconds if it does not refresh automatically.'
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
    }
  }

  async function deleteProvider(providerId) {
    if (!confirm(`Are you sure you want to delete ${providerId}?`)) return
    error = ''; notice = ''
    try {
      const res = await api.providers.delete(providerId)
      notice = res.message || `Deleted provider ${providerId}.`
      restartNeeded = false
      await load()
    } catch (e) {
      error = e.message
    }
  }

  onMount(load)

  const PROVIDER_ICONS = {
    ollama: '🦙', openai: '🤖', anthropic: '🔮', openrouter: '🌐',
    groq: '⚡', mistral: '💨', google: '🔵', deepseek: '🐳',
    together: '🤝', grok: '👁️',
  }
  function providerIcon(id = '') { return PROVIDER_ICONS[id.toLowerCase()] || '⚙️' }

  $: providerList = Object.entries(providers)
  $: doctorFailures = (doctor || []).filter(d => d.status === 'fail').length
  $: doctorWarnings = (doctor || []).filter(d => d.status === 'warn').length
  $: doctorById = Object.fromEntries((doctor || []).map(d => [d.id, d]))
</script>

<div class="page">
  <div class="page-header">
    <h1>Providers &amp; Models</h1>
    <div class="header-actions">
      <button class="btn-primary" on:click={() => openAdd('openai')}>+ Add provider</button>
      <button class="btn-secondary" on:click={runDoctor} disabled={doctorLoading}>
        {doctorLoading ? 'Checking…' : 'Run doctor'}
      </button>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
        <TourButton />
    </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}
  {#if restartNeeded}
    <div class="banner warn restart-banner">
      <span>Provider settings were saved. Restart the gateway to reload provider registrations.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}

  {#if missingKnown.length > 0}
    <div class="suggest-card">
      <span class="suggest-label">Quick add:</span>
      {#each missingKnown as id}
        <button class="suggest-chip" on:click={() => openAdd(id)}>
          {providerIcon(id)} {id}
        </button>
      {/each}
    </div>
  {/if}

  {#if defaultProvider}
    <div class="default-banner">Default provider: <strong>{defaultProvider}</strong></div>
  {/if}

  {#if vaultCheck}
    <div class="doctor-card" class:has-fail={vaultCheck.status === 'fail'} class:has-warn={vaultCheck.status === 'warn'}>
      <div class="doctor-head">
        <div>
          <h2>Vault Consistency</h2>
          <p>{vaultCheck.detail}{vaultCheck.secrets ? ` · ${vaultCheck.secrets} secret${vaultCheck.secrets === 1 ? '' : 's'}` : ''}</p>
        </div>
        <span class="vault-status {vaultCheck.status}">{vaultCheck.status === 'ok' ? '● healthy' : vaultCheck.status === 'warn' ? '▲ warning' : '✕ failing'}</span>
      </div>
      {#if vaultCheck.remedy}<div class="vault-remedy">→ {vaultCheck.remedy}</div>{/if}
    </div>
  {/if}

  {#if doctor.length > 0}
    <div class="doctor-card" class:has-fail={doctorFailures > 0} class:has-warn={doctorWarnings > 0 && doctorFailures === 0}>
      <div class="doctor-head">
        <div>
          <h2>Provider Doctor</h2>
          <p>{doctorFailures} failing, {doctorWarnings} warnings, {doctor.length - doctorFailures - doctorWarnings} healthy</p>
        </div>
        <button class="btn-secondary small-btn" on:click={runDoctor} disabled={doctorLoading}>
          {doctorLoading ? 'Checking…' : 'Recheck'}
        </button>
      </div>
      <div class="doctor-list">
        {#each doctor as check}
          <div class="doctor-row" class:fail={check.status === 'fail'} class:warn={check.status === 'warn'}>
            <div class="doctor-main">
              <span class={'doctor-status ' + check.status}>{check.status}</span>
              <strong>{providerIcon(check.id)} {check.id}</strong>
              <span class="doctor-detail">{check.detail}</span>
            </div>
            <div class="doctor-meta">
              <span>{check.registered ? 'registered' : 'not registered'}</span>
              <span>key: {check.key_source}</span>
              {#if check.model}<span>model: {check.model}</span>{/if}
            </div>
            {#if check.remedy}
              <div class="doctor-remedy">{check.remedy}</div>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if loading}
    <div class="empty">Loading providers…</div>
  {:else if providerList.length === 0}
    <div class="empty-card">
      <div class="empty-icon">⚙️</div>
      <p>No providers configured.</p>
      <p class="hint">Add providers under <code>llm.providers</code> in your config.</p>
    </div>
  {:else}
    <div class="provider-grid">
      {#each providerList as [id, pc]}
        <div class="pv-card" class:default={id === defaultProvider}>
          <div class="pv-header">
            <span class="pv-icon">{providerIcon(id)}</span>
            <div class="pv-identity">
              <span class="pv-name">{id}</span>
              {#if id === defaultProvider}<span class="default-badge">default</span>{/if}
              {#if !pc.registered && pc.api_key}
                <span class="warn-badge" title="Configured in config.yaml but not yet registered. Restart the gateway.">restart needed</span>
              {/if}
            </div>
            <div class="pv-actions">
              <button class="icon-btn-edit" title="Edit credentials" on:click={() => openAdd(id)}>✎</button>
              <button class="icon-btn-delete" title="Delete provider" on:click={() => deleteProvider(id)}>🗑️</button>
            </div>
          </div>

          <div class="pv-body">
            {#if pc.base_url && pc.base_url !== '***'}
              <div class="pv-row"><span class="pv-label">Base URL</span><span class="pv-val mono">{pc.base_url}</span></div>
            {/if}
            <div class="pv-row"><span class="pv-label">Default model</span><span class="pv-val mono">{pc.model || '—'}</span></div>
            <div class="pv-row"><span class="pv-label">API key</span><span class="pv-val">{pc.api_key ? '● Set' : '○ Not set'}</span></div>
            {#if doctorById[id]}
              <div class="pv-row">
                <span class="pv-label">Doctor</span>
                <span class={'pv-val doctor-inline ' + doctorById[id].status}>
                  {doctorById[id].status} · {doctorById[id].key_source}
                </span>
              </div>
            {/if}
            {#if id === 'ollama'}
              <div class="pv-row"><span class="pv-label">Keep alive</span><span class="pv-val mono">{pc.keep_alive || DEFAULT_OLLAMA_KEEP_ALIVE}</span></div>
              <div class="pv-row"><span class="pv-label">Runtime options</span><span class="pv-val mono">{Object.entries({ ...DEFAULT_OLLAMA_OPTIONS, ...(pc.options || {}) }).map(([k, v]) => `${k}=${v}`).join(', ')}</span></div>
            {/if}
            {#if id === 'google'}
              <div class="pv-row"><span class="pv-label">Thinking budget</span><span class="pv-val mono">{pc.thinking_budget === -1 ? 'auto' : (pc.thinking_budget || 0) === 0 ? 'off' : pc.thinking_budget + ' tokens'}</span></div>
              <div class="pv-row"><span class="pv-label">Safety level</span><span class="pv-val">{pc.safety_level || 'default'}</span></div>
            {/if}
            {#if id === 'anthropic'}
              <div class="pv-row"><span class="pv-label">Extended thinking</span><span class="pv-val" class:cache-on={pc.extended_thinking}>{pc.extended_thinking ? `✓ Enabled (${pc.thinking_budget || 8192} tokens)` : '○ Disabled'}</span></div>
            {/if}
            {#if id !== 'ollama' && id !== 'google' && id !== 'anthropic'}
              {#if pc.organization}<div class="pv-row"><span class="pv-label">Organization</span><span class="pv-val mono">{pc.organization}</span></div>{/if}
              <div class="pv-row"><span class="pv-label">Parallel tool calls</span><span class="pv-val">{pc.parallel_tool_calls === false ? 'Serialized' : 'Default (parallel)'}</span></div>
            {/if}
            <div class="pv-row">
              <span class="pv-label">Prompt caching</span>
              <span class="pv-val" class:cache-on={pc.prompt_caching}>{pc.prompt_caching ? '✓ Enabled' : '○ Disabled'}</span>
            </div>

            {#if testResults[id]}
              <div class="pv-row test-result" class:ok={testResults[id].ok} class:fail={!testResults[id].ok && !testResults[id].loading}>
                {#if testResults[id].loading}
                  ⏳ Testing…
                {:else if testResults[id].ok}
                  ✓ {testResults[id].msg}
                {:else}
                  <div class="tr-fail-body">
                    <div class="tr-reason">✗ {testResults[id].msg}</div>
                    {#if testResults[id].fix}
                      <div class="tr-fix">→ {testResults[id].fix}</div>
                    {/if}
                    {#if testResults[id].detail}
                      <button type="button" class="tr-toggle" on:click={() => toggleTestDetail(id)}>
                        {testResults[id].showDetail ? '▾ Hide raw error' : '▸ Show raw error'}
                      </button>
                      {#if testResults[id].showDetail}
                        <pre class="tr-detail">{testResults[id].detail}</pre>
                      {/if}
                    {/if}
                  </div>
                {/if}
              </div>
            {/if}

            {#if models[id]}
              {#if models[id].length === 0}
                <div class="model-empty">No models found. For Ollama, pull one with <code>ollama pull &lt;name&gt;</code>.</div>
              {:else}
                <div class="model-list">
                  <span class="pv-label">Select default model</span>
                  <div class="model-options">
                    {#each models[id] as m}
                      <label class="model-opt" class:sel={chosen[id] === m}>
                        <input type="radio" name={'model-' + id} value={m} bind:group={chosen[id]} />
                        <code>{m}</code>
                        {#if pc.model === m}<span class="cur">current</span>{/if}
                      </label>
                    {/each}
                  </div>
                  <button class="btn-primary small-btn save-model"
                          on:click={() => saveModel(id)}
                          disabled={savingModel[id] || !chosen[id] || chosen[id] === pc.model}>
                    {savingModel[id] ? 'Saving…' : 'Save model'}
                  </button>
                </div>
              {/if}
            {/if}
          </div>

          <div class="pv-footer">
            <button class="btn-secondary small-btn" on:click={() => test(id)}>Test connection</button>
            <button class="btn-secondary small-btn" on:click={() => listModels(id)} disabled={modelsLoading[id]}>
              {modelsLoading[id] ? 'Loading…' : 'List models'}
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <div class="info-card">
    <h3>Adding a provider</h3>
    <p>Click <strong>+ Add provider</strong> above to wire up an API key for OpenAI, Anthropic, or Google. Or edit <code>config.yaml</code> directly under <code>llm.providers</code>:</p>
    <pre class="code-block">llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: http://localhost:11434
      model: llama3
      keep_alive: 30m
      options:
        num_ctx: 4096
        num_batch: 128
    openai:
      api_key: sk-...
      model: gpt-4o-mini
    anthropic:
      api_key: sk-ant-...
      model: claude-3-5-sonnet-latest
    google:
      api_key: AIza...
      model: gemini-2.5-flash</pre>
    <p><strong>List models</strong> queries the live provider. Pick one and <strong>Save model</strong> to persist it to config.yaml. New providers and credential changes require a gateway restart.</p>
  </div>
</div>

{#if showAdd}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close provider modal"
    on:click|self={() => showAdd = false}
    on:keydown={(e) => e.key === 'Escape' && (showAdd = false)}
  >
    <div class="modal">
      <h2>{providers[addId] ? 'Edit' : 'Add'} provider</h2>
      <div class="modal-field">
        <span class="field-label">Provider</span>
        <select bind:value={addId} on:change={() => {
          if (addId === 'custom') {
            customId = ''
            addBase = ''
            addModel = ''
            addKey = ''
            addPromptCaching = false
            addOrganization = ''
            addParallelToolCalls = null
          } else {
            openAdd(addId)
          }
        }}>
          {#each (known.length ? known : ['openai','anthropic','google','ollama']) as id}
            <option value={id}>{providerIcon(id)} {id}</option>
          {/each}
          {#each Object.keys(providers).filter(id => !known.includes(id)) as id}
            <option value={id}>{providerIcon(id)} {id} (custom)</option>
          {/each}
          <option value="custom">➕ Custom OpenAI-compatible...</option>
        </select>
      </div>
      {#if addId === 'custom' || (addId && !known.includes(addId))}
        <div class="modal-field">
          <span class="field-label">Custom Provider ID <span class="req" style="color: #f06060">*</span></span>
          <input
            type="text"
            bind:value={customId}
            placeholder="e.g. deepseek, together, local-vllm"
            disabled={addId !== 'custom'}
          />
          <span class="help">Use lowercase letters, numbers, and dashes. This is the ID you'll use in agent config (e.g. <code>llm.providers.deepseek</code>).</span>
        </div>
      {/if}
      <div class="modal-field">
        <span class="field-label">Base URL <span class="opt">(optional)</span></span>
        <input type="text" bind:value={addBase} placeholder={KNOWN_BASES[addId] || 'https://api.example.com/v1'} />
      </div>
      <div class="modal-field">
        <span class="field-label">API key</span>
        <input type="password" bind:value={addKey} placeholder={providers[addId]?.api_key ? 'Leave blank to keep current key' : 'sk-...'} />
      </div>
      <div class="modal-field">
        <span class="field-label">Default model</span>
        <input type="text" bind:value={addModel} placeholder={KNOWN_MODELS[addId] || 'model-name'} />
      </div>
      <!-- Prompt caching — all providers -->
      <div class="anthropic-tuning">
        <div class="tuning-head"><span>Cost optimisation</span></div>
        <label class="cache-toggle">
          <input type="checkbox" bind:checked={addPromptCaching} />
          <span class="cache-toggle-label">
            Enable prompt caching
            <span class="cache-badge">{addPromptCaching ? 'on' : 'off'}</span>
          </span>
        </label>
        <p class="help">Caches the system prompt and tool definitions between turns so they aren't re-billed on every request. On Anthropic, cache hits cost 10% of normal input price (≥ 1024 tokens required).</p>
      </div>

      <!-- Google-specific tuning -->
      {#if addId === 'google'}
        <div class="provider-tuning">
          <div class="tuning-head"><span>Google Gemini settings</span></div>
          <label class="modal-field">
            <span class="field-label">Thinking budget</span>
            <select bind:value={addThinkingBudget}>
              <option value={0}>Off — fast, no reasoning trace (default)</option>
              <option value={-1}>Auto — model decides</option>
              <option value={1024}>1 K tokens</option>
              <option value={4096}>4 K tokens</option>
              <option value={8192}>8 K tokens</option>
              <option value={16000}>16 K tokens</option>
            </select>
            <span class="help">Controls Gemini 2.5 extended thinking. Higher budgets improve complex reasoning but increase latency and cost.</span>
          </label>
          <label class="modal-field">
            <span class="field-label">Safety level</span>
            <select bind:value={addSafetyLevel}>
              <option value="">Default — Gemini built-in filters</option>
              <option value="off">Off — BLOCK_NONE (recommended for agents)</option>
              <option value="strict">Strict — BLOCK_LOW_AND_ABOVE</option>
            </select>
            <span class="help">"Off" disables content filters on all harm categories. Recommended for agent/developer use to prevent false blocks on technical content.</span>
          </label>
        </div>
      {/if}

      <!-- Anthropic-specific tuning -->
      {#if addId === 'anthropic'}
        <div class="provider-tuning">
          <div class="tuning-head"><span>Anthropic settings</span></div>
          <label class="cache-toggle">
            <input type="checkbox" bind:checked={addExtendedThinking} />
            <span class="cache-toggle-label">
              Extended thinking (Claude 3.7+)
              <span class="cache-badge">{addExtendedThinking ? 'on' : 'off'}</span>
            </span>
          </label>
          {#if addExtendedThinking}
            <label class="modal-field" style="margin-top:.5rem">
              <span class="field-label">Thinking budget (tokens)</span>
              <input type="number" bind:value={addAnthropicThinkingBudget} min="1024" max="100000" placeholder="8192" />
              <span class="help">Token budget for internal reasoning. Higher values improve complex tasks but increase latency. Requires claude-3-7-sonnet or newer.</span>
            </label>
          {/if}
        </div>
      {/if}

      <!-- OpenAI / compatible provider tuning -->
      {#if addId !== 'ollama' && addId !== 'google' && addId !== 'anthropic'}
        <div class="provider-tuning">
          <div class="tuning-head"><span>OpenAI / compatible settings</span></div>
          <label class="modal-field">
            <span class="field-label">Organization ID <span class="opt">(optional)</span></span>
            <input type="text" bind:value={addOrganization} placeholder="org-..." />
            <span class="help">Sent as the OpenAI-Organization header. Required for some enterprise/team accounts.</span>
          </label>
          <label class="modal-field">
            <span class="field-label">Parallel tool calls</span>
            <select on:change={(e) => addParallelToolCalls = e.currentTarget.value === '' ? null : e.currentTarget.value === 'true'}>
              <option value="" selected={addParallelToolCalls === null}>Default (parallel)</option>
              <option value="false" selected={addParallelToolCalls === false}>Serialized — one tool at a time</option>
            </select>
            <span class="help">Serialized mode prevents the model from calling multiple tools simultaneously. Reduces agent loop failures on some models.</span>
          </label>
        </div>
      {/if}

      {#if addId === 'ollama'}
        <div class="ollama-tuning">
          <div class="tuning-head">
            <span>Ollama performance tuning</span>
            <div class="preset-row">
              <button class="tiny-chip" type="button" on:click={() => applyOllamaPreset('balanced')}>Balanced</button>
              <button class="tiny-chip" type="button" on:click={() => applyOllamaPreset('long')}>Long context</button>
              <button class="tiny-chip" type="button" on:click={() => applyOllamaPreset('resident')}>Keep loaded</button>
              <button class="tiny-chip" type="button" on:click={() => applyOllamaPreset('clear')}>Reset defaults</button>
            </div>
          </div>
          <label class="modal-field">
            <span class="field-label">Keep alive</span>
            <input type="text" bind:value={addKeepAlive} placeholder="30m, 24h, -1, or 0" />
            <span class="help">How long Ollama keeps the model in memory after a request. Use <code>-1</code> for resident models; use <code>0</code> to unload immediately.</span>
          </label>
          <div class="option-grid">
            {#each OLLAMA_OPTION_FIELDS as f}
              <label class="modal-field">
                <span class="field-label">{f.label}</span>
                {#if f.type === 'boolean'}
                  <select value={ollamaOptions[f.key] === undefined ? '' : String(ollamaOptions[f.key])} on:change={(e) => updateOllamaOption(f.key, e.currentTarget.value, f.type)}>
                    <option value="">Default</option>
                    <option value="true">True</option>
                    <option value="false">False</option>
                  </select>
                {:else}
                  <input type="number" value={ollamaOptions[f.key] ?? ''} placeholder={f.placeholder || ''} on:input={(e) => updateOllamaOption(f.key, e.currentTarget.value, f.type)} />
                {/if}
                <span class="help">{f.help}</span>
              </label>
            {/each}
          </div>
          <label class="modal-field">
            <span class="field-label">Advanced options JSON</span>
            <textarea rows="3" bind:value={ollamaOptionsJSON} placeholder={ADVANCED_OPTIONS_PLACEHOLDER}></textarea>
            <span class="help">Merged into Ollama <code>options</code>. Use this for newer Ollama options not yet in the form.</span>
          </label>
          <div class="server-hints">
            Server-level Ollama settings are outside this API request. For throughput/memory tuning, start Ollama with <code>OLLAMA_NUM_PARALLEL</code>, <code>OLLAMA_MAX_LOADED_MODELS</code>, <code>OLLAMA_MAX_QUEUE</code>, <code>OLLAMA_FLASH_ATTENTION=1</code>, or <code>OLLAMA_KV_CACHE_TYPE</code>.
          </div>
        </div>
      {/if}
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showAdd = false}>Cancel</button>
        <button class="btn-primary" on:click={saveCreds} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
      <p class="modal-hint">Settings persist to <code>config.yaml</code>. Restart the gateway for new providers to be registered.</p>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }

  .suggest-card {
    display: flex; flex-wrap: wrap; align-items: center; gap: .5rem;
    background: rgba(108,99,255,.06); border: 1px solid rgba(108,99,255,.18);
    padding: .6rem .85rem; border-radius: 8px; font-size: .82rem;
  }
  .suggest-label { color: #8b85ff; font-weight: 600; margin-right: .25rem; }
  .suggest-chip  {
    background: #1c1f35; color: #c8cadf; border: 1px solid #2a2f4a;
    padding: .25rem .7rem; border-radius: 999px; font-size: .78rem;
    cursor: pointer; transition: background .12s, border-color .12s;
  }
  .suggest-chip:hover { background: #252840; border-color: #6c63ff; color: #e8eaf6; }

  .warn-badge {
    font-size: .65rem; padding: .12rem .45rem; border-radius: 999px;
    background: rgba(240,180,80,.18); color: #f0c060; font-weight: 600;
  }
  .pv-actions { margin-left: auto; display: flex; gap: .25rem; }
  .icon-btn-edit, .icon-btn-delete {
    background: none; color: #6b7294;
    padding: .2rem .4rem; border-radius: 4px; font-size: .9rem;
  }
  .icon-btn-edit:hover { color: #e8eaf6; background: #1a1e36; }
  .icon-btn-delete:hover { color: #f06060; background: rgba(240,96,96,.1); }

  .modal-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 760px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: .9rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal-field { display: flex; flex-direction: column; gap: .35rem; }
  .field-label { font-size: .78rem; color: #7b82a8; }
  .modal-field .opt { color: #555a7a; font-weight: normal; }
  .modal-field textarea {
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 6px; padding: .5rem .65rem; font-size: .85rem;
    resize: vertical; min-height: 4.5rem; font-family: monospace;
  }
  .help { font-size: .7rem; color: #6b7294; line-height: 1.45; }
  .help code { background: #1c1f35; padding: .05rem .25rem; border-radius: 4px; color: #8b85ff; }
  .modal-row {
    display: flex; gap: .75rem; justify-content: flex-end; margin-top: .25rem;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }
  .modal-hint { font-size: .72rem; color: #555a7a; }
  .modal-hint code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }
  .cache-on { color: #4caf82 !important; }

  .anthropic-tuning, .provider-tuning {
    margin-top: .25rem; padding: .9rem; border: 1px solid #1a1e36; border-radius: 8px;
    background: #101323; display: flex; flex-direction: column; gap: .75rem;
  }
  .cache-toggle {
    display: flex; align-items: center; gap: .65rem; cursor: pointer;
  }
  .cache-toggle input[type="checkbox"] { width: 1rem; height: 1rem; accent-color: #6c63ff; cursor: pointer; }
  .cache-toggle-label { font-size: .85rem; color: #c8cadf; display: flex; align-items: center; gap: .5rem; }
  .cache-badge {
    font-size: .68rem; padding: .1rem .4rem; border-radius: 999px;
    background: rgba(76,175,130,.15); color: #4caf82; font-weight: 600;
  }

  .ollama-tuning {
    margin-top: .25rem; padding: .9rem; border: 1px solid #1a1e36; border-radius: 8px;
    background: #101323; display: flex; flex-direction: column; gap: .85rem;
  }
  .tuning-head { display: flex; align-items: flex-start; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .tuning-head > span { font-size: .85rem; color: #e0e1f0; font-weight: 600; }
  .preset-row { display: flex; gap: .35rem; flex-wrap: wrap; }
  .tiny-chip {
    background: #1c1f35; color: #c8cadf; border: 1px solid #2a2f4a;
    padding: .22rem .5rem; border-radius: 999px; font-size: .7rem; cursor: pointer;
  }
  .tiny-chip:hover { border-color: #6c63ff; color: #e8eaf6; }
  .option-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .75rem; }

  @media (max-width: 640px) {
    .option-grid { grid-template-columns: 1fr; }
  }
  .server-hints {
    background: rgba(240,196,96,.06); border: 1px solid rgba(240,196,96,.18);
    color: #b9a36d; border-radius: 7px; padding: .7rem .8rem; font-size: .74rem; line-height: 1.55;
  }
  .server-hints code { background: #1c1f35; padding: .05rem .25rem; border-radius: 4px; color: #f0c460; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .empty  { color: #6b7294; padding: 3rem; text-align: center; }

  .default-banner {
    background: rgba(108,99,255,.08); border: 1px solid rgba(108,99,255,.2);
    border-radius: 8px; padding: .6rem 1rem; font-size: .85rem; color: #8b85ff;
  }
  .doctor-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .95rem 1rem; display: flex; flex-direction: column; gap: .85rem;
  }
  .doctor-card.has-warn { border-color: rgba(240,196,96,.28); }
  .doctor-card.has-fail { border-color: rgba(240,96,96,.32); }
  .vault-status { font-size: .74rem; font-weight: 700; }
  .vault-status.ok { color: #5fce9a; }
  .vault-status.warn { color: #e6c060; }
  .vault-status.fail { color: #f06060; }
  .vault-remedy { margin-top: .5rem; font-size: .76rem; color: #8a91b8; }
  .doctor-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .doctor-head h2 { font-size: .95rem; font-weight: 600; margin-bottom: .2rem; }
  .doctor-head p { font-size: .76rem; color: #7b82a8; }
  .doctor-list { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: .6rem; }
  .doctor-row {
    border: 1px solid #1f2440; background: #101323; border-radius: 8px;
    padding: .7rem .75rem; display: flex; flex-direction: column; gap: .45rem;
  }
  .doctor-row.warn { border-color: rgba(240,196,96,.24); background: rgba(240,196,96,.04); }
  .doctor-row.fail { border-color: rgba(240,96,96,.28); background: rgba(240,96,96,.045); }
  .doctor-main { display: flex; align-items: center; flex-wrap: wrap; gap: .45rem; font-size: .82rem; }
  .doctor-detail { color: #9aa0c1; }
  .doctor-status {
    text-transform: uppercase; font-size: .62rem; line-height: 1;
    padding: .25rem .38rem; border-radius: 999px; font-weight: 700;
  }
  .doctor-status.ok { background: rgba(76,175,130,.14); color: #4caf82; }
  .doctor-status.warn { background: rgba(240,196,96,.14); color: #f0c460; }
  .doctor-status.fail { background: rgba(240,96,96,.14); color: #f06060; }
  .doctor-meta { display: flex; flex-wrap: wrap; gap: .35rem; }
  .doctor-meta span {
    color: #7b82a8; background: #1a1e36; border: 1px solid #252a48;
    border-radius: 999px; padding: .16rem .45rem; font-size: .68rem;
  }
  .doctor-remedy {
    color: #c6ad69; font-size: .73rem; line-height: 1.45;
    padding-top: .15rem;
  }
  .empty-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 3rem 2rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .75rem; color: #6b7294;
  }
  .empty-icon { font-size: 2.5rem; }
  .hint { font-size: .82rem; }
  .hint code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  .provider-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 1rem; }

  .pv-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden; transition: border-color .2s;
  }
  .pv-card.default { border-color: rgba(108,99,255,.4); }

  .pv-header { display: flex; align-items: center; gap: .75rem; padding: .9rem 1rem; border-bottom: 1px solid #1a1e36; }
  .pv-icon     { font-size: 1.5rem; }
  .pv-identity { display: flex; align-items: center; gap: .5rem; }
  .pv-name     { font-weight: 600; font-size: .95rem; }
  .default-badge { font-size: .68rem; padding: .12rem .45rem; border-radius: 999px; background: rgba(108,99,255,.2); color: #8b85ff; font-weight: 600; }

  .pv-body  { flex: 1; padding: .75rem 1rem; display: flex; flex-direction: column; gap: .45rem; }
  .pv-row   { display: flex; justify-content: space-between; align-items: flex-start; font-size: .82rem; gap: .5rem; }
  .pv-label { color: #555a7a; flex-shrink: 0; }
  .pv-val   { color: #c8cadf; text-align: right; word-break: break-all; }
  .pv-val.mono { font-family: monospace; font-size: .78rem; color: #8b85ff; }
  .doctor-inline.ok { color: #4caf82; }
  .doctor-inline.warn { color: #f0c460; }
  .doctor-inline.fail { color: #f06060; }

  .test-result { font-size: .78rem; padding: .35rem .5rem; border-radius: 6px; margin-top: .25rem; background: #1a1e36; }
  .test-result.ok   { color: #4caf82; }
  .test-result.fail { color: #f06060; }
  /* E4 — friendly provider-error card layout */
  .tr-fail-body { display: flex; flex-direction: column; gap: .3rem; }
  .tr-reason    { font-weight: 600; }
  .tr-fix       { color: #c8c7ff; font-weight: 400; }
  .tr-toggle    {
    align-self: flex-start; background: transparent; border: none;
    color: #8b85ff; font-size: .74rem; padding: 0; cursor: pointer;
  }
  .tr-toggle:hover { text-decoration: underline; }
  .tr-detail    {
    background: #10132a; color: #d6d4e6; padding: .4rem .55rem;
    border-radius: 4px; font-size: .72rem; margin: .15rem 0 0;
    white-space: pre-wrap; word-break: break-word; max-height: 180px; overflow: auto;
  }

  .model-empty { font-size: .76rem; color: #6b7294; margin-top: .35rem; }
  .model-empty code { background: #1a1e36; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }

  .model-list { margin-top: .5rem; padding-top: .5rem; border-top: 1px solid #1a1e36; display: flex; flex-direction: column; gap: .4rem; }
  .model-options { display: flex; flex-direction: column; gap: .25rem; max-height: 220px; overflow-y: auto; }
  .model-opt {
    display: flex; align-items: center; gap: .5rem; padding: .3rem .45rem;
    border-radius: 6px; cursor: pointer; border: 1px solid transparent;
  }
  .model-opt:hover { background: #1a1e36; }
  .model-opt.sel  { background: rgba(108,99,255,.12); border-color: rgba(108,99,255,.35); }
  .model-opt input { width: auto; }
  .model-opt code { font-size: .77rem; color: #b0b5d8; }
  .cur { font-size: .65rem; color: #4caf82; margin-left: auto; }
  .save-model { align-self: flex-start; margin-top: .25rem; }

  .pv-footer { padding: .75rem 1rem; border-top: 1px solid #1a1e36; display: flex; gap: .5rem; justify-content: flex-end; }
  .small-btn { padding: .3rem .75rem; font-size: .78rem; border-radius: 6px; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .85rem 1rem; font-family: monospace; font-size: .78rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre;
  }
</style>
