<script>
  import TourButton from '../lib/TourButton.svelte'
  // Guided first-run wizard: provider → test → template → (optional) channel →
  // production updates → create + launch. Reuses existing endpoints only; every
  // failure is shown as a plain-language fix, never a raw error.
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  const STEPS = ['Provider', 'Test', 'Template', 'Channel', 'Updates', 'Launch']
  const DEFAULT_UPDATE_MANIFEST = 'https://github.com/vmodekurti/soulacy-commercial/releases/latest/download/release-manifest.json'
  const FOCUS_OPTIONS = [
    { id: 'research', label: 'Daily research', terms: ['research', 'brief', 'market', 'stock', 'monitor', 'weather', 'news'] },
    { id: 'work', label: 'Inbox & meetings', terms: ['inbox', 'meeting', 'minutes', 'triage', 'action'] },
    { id: 'docs', label: 'Docs & knowledge', terms: ['document', 'compliance', 'knowledge', 'rag', 'audit', 'policy'] },
    { id: 'deals', label: 'Deals & monitors', terms: ['deal', 'flight', 'price', 'finder', 'monitor', 'alert'] },
    { id: 'custom', label: 'Custom workflow', terms: ['basic', 'chat', 'starter', 'workflow'] },
  ]
  let step = 0
  let loading = true
  let loadError = ''

  // Step 1 — provider
  let knownProviders = []
  let configured = {}          // id → { model, registered, base_url, api_key }
  let providerId = ''
  let apiKey = ''
  let baseUrl = ''
  let model = ''
  let savingProvider = false
  let providerFix = ''

  // Step 2 — test
  let testing = false
  let testOk = false
  let testFix = ''
  let models = []

  // Step 3 — template
  let templates = []
  let templateName = ''
  let templatesFix = ''
  let assistantFocus = 'research'

  // Step 4 — channel (optional)
  let wantChannel = false
  let tgToken = ''
  let tgChatId = ''
  let channelSaving = false
  let channelFix = ''
  let channelDone = false

  // Step 5 — production updates
  let currentUpdateManifest = ''
  let updateManifest = DEFAULT_UPDATE_MANIFEST
  let savingUpdates = false
  let updateFix = ''
  let updatesDone = false

  // Step 6 — launch
  let launching = false
  let createdId = ''
  let launchFix = ''

  const needsKey = (id) => id !== 'ollama'
  const localProvider = (id) => id === 'ollama'

  async function load() {
    loading = true; loadError = ''
    try {
      const [p, t, c] = await Promise.all([
        api.providers.list(),
        api.templates.list().catch(() => ({ templates: [] })),
        api.config.get().catch(() => ({})),
      ])
      knownProviders = p.known || Object.keys(p.providers || {})
      configured = p.providers || {}
      templates = t.templates || []
      currentUpdateManifest = c?.updates?.manifest_url || ''
      updateManifest = currentUpdateManifest || DEFAULT_UPDATE_MANIFEST
      updatesDone = !!currentUpdateManifest
      // Preselect a sensible default: an already-configured provider, else first known.
      const already = Object.keys(configured).find(id => configured[id]?.registered)
      providerId = already || knownProviders[0] || 'openai'
      syncProviderFields()
      try { assistantFocus = localStorage.getItem('soulacy-assistant-focus') || assistantFocus } catch (_) {}
      pickRecommendedTemplate()
    } catch (e) {
      loadError = fixHint(e, 'load')
    } finally {
      loading = false
    }
  }

  function syncProviderFields() {
    const c = configured[providerId] || {}
    baseUrl = c.base_url || (localProvider(providerId) ? 'http://localhost:11434' : '')
    model = c.model || ''
    apiKey = ''            // never prefill secrets; blank means "keep existing"
    testOk = false; models = []; testFix = ''
  }

  // fixHint turns a raw error into an actionable, plain-language fix.
  function fixHint(e, ctx) {
    const raw = (e && (e.message || String(e))) || ''
    const s = raw.toLowerCase()
    if (s.includes('401') || s.includes('unauthorized') || s.includes('invalid_api_key') || s.includes('invalid api key'))
      return 'The API key was rejected. Double-check you pasted the full key for this provider and try again.'
    if (s.includes('403') || s.includes('forbidden'))
      return 'This key is valid but not permitted for that model. Enable the model for your account or pick another.'
    if (s.includes('model') && (s.includes('not found') || s.includes('does not exist') || s.includes('unknown')))
      return 'That model name isn’t available on this key. Run the test to list the models you can use, then pick one.'
    if (s.includes('connection refused') || s.includes('dial') || s.includes('econnrefused') || s.includes('no such host'))
      return localProvider(providerId)
        ? 'Couldn’t reach the local provider. Make sure Ollama is running (`ollama serve`) and the base URL is correct.'
        : 'Couldn’t reach the provider. Check the base URL and your network connection.'
    if (s.includes('timeout') || s.includes('deadline'))
      return 'The provider took too long to respond. Check your connection and try again.'
    if (ctx === 'template') return 'Couldn’t install the template. ' + (raw || 'Please try another template.')
    if (ctx === 'channel') return 'Couldn’t save the channel. Check the bot token and destination ID.'
    if (ctx === 'updates') return 'Couldn’t save the update manifest. Check that config.yaml is writable, or set SOULACY_UPDATE_MANIFEST before launch.'
    return raw || 'Something went wrong. Please try again.'
  }

  async function saveProvider() {
    providerFix = ''
    if (needsKey(providerId) && !apiKey && !configured[providerId]?.registered) {
      providerFix = 'Enter the API key for ' + providerId + ' to continue.'
      return
    }
    savingProvider = true
    try {
      const body = {}
      if (apiKey) body.api_key = apiKey
      if (baseUrl) body.base_url = baseUrl
      if (model) body.model = model
      await api.providers.setCredentials(providerId, body)
      step = 1
    } catch (e) {
      providerFix = fixHint(e, 'provider')
    } finally {
      savingProvider = false
    }
  }

  async function testProvider() {
    testing = true; testOk = false; testFix = ''; models = []
    try {
      const res = await api.providers.models(providerId)
      models = res.models || []
      testOk = true
      // If the user hasn't chosen a model yet, default to the first offered.
      if (!model && models.length) model = models[0].id || models[0].name || models[0]
    } catch (e) {
      testFix = fixHint(e, 'test')
    } finally {
      testing = false
    }
  }

  async function saveModelAndContinue() {
    // Persist the chosen model (in case it changed after listing) then advance.
    try {
      if (model) await api.providers.setModel(providerId, model)
    } catch (_) { /* non-fatal: instantiate still works with provider default */ }
    step = 2
  }

  async function saveChannel() {
    channelFix = ''
    if (!tgToken) { channelFix = 'Paste your Telegram bot token, or skip this step.'; return }
    channelSaving = true
    try {
      await api.channels.update('telegram', {
        settings: { token: tgToken, default_output_to: tgChatId, outbound_only: true },
      })
      await api.channels.enable('telegram').catch(() => {})
      channelDone = true
      step = 4
    } catch (e) {
      channelFix = fixHint(e, 'channel')
    } finally {
      channelSaving = false
    }
  }

  async function saveUpdates() {
    updateFix = ''
    savingUpdates = true
    try {
      const value = (updateManifest || '').trim()
      if (value) {
        await api.config.patch({ updates: { manifest_url: value } })
        currentUpdateManifest = value
        updatesDone = true
      } else {
        updatesDone = false
      }
      step = 5
    } catch (e) {
      updateFix = fixHint(e, 'updates')
    } finally {
      savingUpdates = false
    }
  }

  async function launch(openIn) {
    launching = true; launchFix = ''
    try {
      const opts = {}
      if (wantChannel && channelDone) {
        opts.output = { channel: 'telegram' }
        if (tgChatId) opts.output.to = tgChatId
      }
      const def = await api.templates.instantiate(templateName, opts)
      createdId = def.id || (def.def && def.def.id) || ''
      // Hand the new agent to Chat so it opens already selected.
      try { if (createdId) localStorage.setItem('soulacy-preselect-agent', createdId) } catch (_) {}
      try { localStorage.setItem('soulacy-onboarding-seen', '1') } catch (_) {}
      try { localStorage.setItem('soulacy-assistant-focus', assistantFocus) } catch (_) {}
      window.location.hash = '#' + (openIn || 'chat')
    } catch (e) {
      launchFix = fixHint(e, 'template')
    } finally {
      launching = false
    }
  }

  function skipChannel() { wantChannel = false; step = 4 }
  function skipUpdates() { updatesDone = false; step = 5 }
  function go(page) { if (page) window.location.hash = '#' + page }

  function focusTerms() {
    return (FOCUS_OPTIONS.find(x => x.id === assistantFocus)?.terms) || []
  }

  function templateText(t) {
    return [
      t?.name,
      t?.display_name,
      t?.description,
      ...(t?.tags || []),
    ].join(' ').toLowerCase()
  }

  function templateScore(t) {
    const text = templateText(t)
    let score = 0
    for (const term of focusTerms()) {
      if (text.includes(term)) score += 2
    }
    if ((t?.tags || []).includes('workflow')) score += 1
    if (assistantFocus === 'custom' && !(t?.tags || []).includes('workflow')) score += 2
    return score
  }

  function rankTemplates(list) {
    return [...(list || [])].sort((a, b) => templateScore(b) - templateScore(a) || String(a.display_name || a.name).localeCompare(String(b.display_name || b.name)))
  }

  function pickRecommendedTemplate() {
    const ranked = rankTemplates(templates)
    if (ranked.length && (!templateName || !ranked.some(t => t.name === templateName))) {
      templateName = ranked[0].name
    }
  }

  function chooseFocus(id) {
    assistantFocus = id
    const ranked = rankTemplates(templates)
    if (ranked.length) templateName = ranked[0].name
  }

  $: selectedTemplate = templates.find(t => t.name === templateName)
  $: recommendedTemplates = rankTemplates(templates)
  onMount(load)
</script>

<div class="wiz">
  <div class="wiz-head">
    <h1>Welcome to Soulacy</h1>
    <p>Let’s get your first agent running. This takes about five minutes.</p>
    <TourButton />
  </div>

  <ol class="stepper">
    {#each STEPS as label, i}
      <li class:active={i === step} class:done={i < step}>
        <span class="dot">{i < step ? '✓' : i + 1}</span>
        <span class="lbl">{label}</span>
      </li>
    {/each}
  </ol>

  {#if loadError}<div class="banner err">{loadError} <button class="link" on:click={load}>Retry</button></div>{/if}

  {#if loading}
    <div class="card"><p class="muted">Checking your workspace…</p></div>
  {:else if step === 0}
    <div class="card">
      <h2>Connect an LLM provider</h2>
      <p class="muted">Pick who runs the model. Local (Ollama) needs no key; cloud providers need an API key.</p>
      <label>Provider
        <select bind:value={providerId} on:change={syncProviderFields}>
          {#each knownProviders as id}
            <option value={id}>{id}{configured[id]?.registered ? ' — configured' : ''}</option>
          {/each}
        </select>
      </label>
      {#if needsKey(providerId)}
        <label>API key
          <input type="password" bind:value={apiKey}
                 placeholder={configured[providerId]?.registered ? '•••••• (leave blank to keep)' : 'Paste your API key'} />
        </label>
      {/if}
      <label>Base URL <span class="opt">(optional)</span>
        <input bind:value={baseUrl} placeholder={localProvider(providerId) ? 'http://localhost:11434' : 'Provider default'} />
      </label>
      <label>Model <span class="opt">(optional — you can pick after testing)</span>
        <input bind:value={model} placeholder="e.g. gpt-4o-mini, llama3, claude-3-5-sonnet" />
      </label>
      {#if providerFix}<div class="banner err">{providerFix}</div>{/if}
      <div class="actions">
        <button class="btn-primary" on:click={saveProvider} disabled={savingProvider}>
          {savingProvider ? 'Saving…' : 'Save & continue'}
        </button>
      </div>
    </div>

  {:else if step === 1}
    <div class="card">
      <h2>Test the connection</h2>
      <p class="muted">We’ll ask {providerId} for its model list. If this works, your credentials are good.</p>
      <div class="actions">
        <button class="btn-primary" on:click={testProvider} disabled={testing}>
          {testing ? 'Testing…' : 'Run test'}
        </button>
        <button class="link" on:click={() => (step = 0)}>← Back</button>
      </div>
      {#if testOk}
        <div class="banner ok">Connected ✓ — {providerId} responded with {models.length} model{models.length === 1 ? '' : 's'}.</div>
        {#if models.length}
          <label>Choose a model
            <select bind:value={model}>
              {#each models as m}
                <option value={m.id || m.name || m}>{m.id || m.name || m}</option>
              {/each}
            </select>
          </label>
        {/if}
        <div class="actions">
          <button class="btn-primary" on:click={saveModelAndContinue}>Continue</button>
        </div>
      {/if}
      {#if testFix}<div class="banner err">{testFix}</div>{/if}
    </div>

  {:else if step === 2}
    <div class="card">
      <h2>Pick a starter</h2>
      <p class="muted">What should Soulacy help with first? I’ll sort the vetted starters around that goal.</p>
      <div class="focus-row">
        {#each FOCUS_OPTIONS as option}
          <button class:sel={assistantFocus === option.id} on:click={() => chooseFocus(option.id)}>{option.label}</button>
        {/each}
      </div>
      <div class="tpl-grid">
        {#each recommendedTemplates as t}
          <button class="tpl {templateName === t.name ? 'sel' : ''}" on:click={() => (templateName = t.name)}>
            <strong>{t.display_name || t.name}</strong>
            <small>{t.description || ''}</small>
          </button>
        {:else}
          <p class="muted">No templates available.</p>
        {/each}
      </div>
      {#if templatesFix}<div class="banner err">{templatesFix}</div>{/if}
      <div class="actions">
        <button class="link" on:click={() => (step = 1)}>← Back</button>
        <button class="btn-primary" on:click={() => (step = 3)} disabled={!templateName}>Continue</button>
      </div>
    </div>

  {:else if step === 3}
    <div class="card">
      <h2>Send results to a channel <span class="opt">(optional)</span></h2>
      <p class="muted">Deliver this agent’s output to Telegram. You can skip this and add channels later.</p>
      <label class="check">
        <input type="checkbox" bind:checked={wantChannel} /> Set up Telegram now
      </label>
      {#if wantChannel}
        <label>Bot token
          <input bind:value={tgToken} placeholder="123456:ABC-… from @BotFather" />
        </label>
        <label>Destination chat ID <span class="opt">(where messages go)</span>
          <input bind:value={tgChatId} placeholder="e.g. 987654321" />
        </label>
        <p class="hint">Create a bot with <code>@BotFather</code>, then message your bot once so it can find the chat ID.</p>
        {#if channelFix}<div class="banner err">{channelFix}</div>{/if}
      {/if}
      <div class="actions">
        <button class="link" on:click={() => (step = 2)}>← Back</button>
        {#if wantChannel}
          <button class="btn-primary" on:click={saveChannel} disabled={channelSaving}>
            {channelSaving ? 'Saving…' : 'Save channel & continue'}
          </button>
        {:else}
          <button class="btn-primary" on:click={skipChannel}>Skip for now</button>
        {/if}
      </div>
    </div>

  {:else if step === 4}
    <div class="card">
      <h2>Configure production updates</h2>
      <p class="muted">
        Soulacy can check a verified release manifest before installing updates. This keeps production installs upgradeable without guessing which bundle to use.
      </p>
      <label>Release manifest URL or local path
        <input bind:value={updateManifest} placeholder={DEFAULT_UPDATE_MANIFEST} />
      </label>
      <p class="hint">
        Default: <code>{DEFAULT_UPDATE_MANIFEST}</code>. Local/private deployments can point this at an internal manifest file or HTTPS URL.
      </p>
      {#if currentUpdateManifest}
        <div class="banner ok">Current manifest saved: <code>{currentUpdateManifest}</code></div>
      {/if}
      {#if updateFix}<div class="banner err">{updateFix}</div>{/if}
      <div class="actions">
        <button class="link" on:click={() => (step = 3)}>← Back</button>
        <button class="btn-primary" on:click={saveUpdates} disabled={savingUpdates}>
          {savingUpdates ? 'Saving…' : 'Save updates & continue'}
        </button>
        <button class="btn-secondary" on:click={skipUpdates}>Skip for now</button>
      </div>
    </div>

  {:else if step === 5}
    <div class="card">
      <h2>Create & launch</h2>
      <p class="muted">
        We’ll create <strong>{selectedTemplate?.display_name || templateName}</strong>
        on <strong>{providerId}</strong>{model ? ` (${model})` : ''}{wantChannel && channelDone ? ', wired to Telegram' : ''}{updatesDone ? ', update-ready' : ''}.
      </p>
      {#if channelDone}<div class="banner ok">Telegram saved and connected. No gateway restart is needed.</div>{/if}
      {#if updatesDone}<div class="banner ok">Updates configured — verify any time with <code>sy update check</code>.</div>{/if}
      {#if launchFix}<div class="banner err">{launchFix}</div>{/if}
      <div class="actions">
        <button class="link" on:click={() => (step = 4)}>← Back</button>
        <button class="btn-primary" on:click={() => launch('chat')} disabled={launching}>
          {launching ? 'Creating…' : 'Create & open Chat'}
        </button>
        <button class="btn-secondary" on:click={() => launch('studio')} disabled={launching}>
          Create & open Studio
        </button>
      </div>
    </div>
  {/if}

  <div class="wiz-foot">
    <button class="link" on:click={() => go('dashboard')}>Skip setup</button>
  </div>
</div>

<style>
  .wiz { max-width: 720px; margin: 0 auto; display: flex; flex-direction: column; gap: 16px; }
  .wiz-head h1 { margin: 0; font-size: 1.5rem; }
  .wiz-head p, .muted { color: #8f96bb; font-size: .88rem; }
  .stepper { display: flex; list-style: none; padding: 0; margin: 0; gap: 6px; }
  .stepper li { flex: 1; display: flex; flex-direction: column; align-items: center; gap: 6px; color: #6b7196; font-size: .72rem; }
  .stepper .dot { width: 28px; height: 28px; border-radius: 50%; display: grid; place-items: center; background: #171a2e; border: 1px solid #2a2f52; color: #8f96bb; font-size: .78rem; }
  .stepper li.active .dot { background: #6c5cff; border-color: #6c5cff; color: #fff; }
  .stepper li.done .dot { background: rgba(96,200,120,.18); border-color: rgba(96,200,120,.5); color: #60c878; }
  .stepper li.active .lbl { color: #cfd3ee; }
  .card { border: 1px solid #232743; background: #111426; border-radius: 12px; padding: 20px; display: flex; flex-direction: column; gap: 12px; }
  .card h2 { margin: 0; font-size: 1.1rem; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: .8rem; color: #b7bce0; }
  label.check { flex-direction: row; align-items: center; gap: 8px; }
  input, select { padding: .5rem .6rem; border-radius: 8px; border: 1px solid #2a2f52; background: #0d0f1c; color: #e6e8f5; font-size: .86rem; }
  .opt { color: #6b7196; font-weight: 400; }
  .hint { color: #8f96bb; font-size: .76rem; }
  .hint code, .card code { background: #0d0f1c; padding: .05rem .3rem; border-radius: 4px; }
  .actions { display: flex; gap: 10px; align-items: center; margin-top: 4px; flex-wrap: wrap; }
  .banner { padding: .6rem .75rem; border-radius: 8px; font-size: .82rem; }
  .banner.err { color: #ff9a9a; background: rgba(255,90,90,.12); }
  .banner.ok { color: #72d9aa; background: rgba(76,175,130,.12); }
  .link { background: none; border: none; color: #8b85ff; cursor: pointer; font-size: .82rem; padding: 0; }
  .tpl-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: 10px; }
  .focus-row { display: flex; gap: 8px; flex-wrap: wrap; }
  .focus-row button { border: 1px solid #2a2f52; background: #0d0f1c; color: #b7bce0; border-radius: 999px; padding: .4rem .65rem; cursor: pointer; font-size: .78rem; }
  .focus-row button.sel { border-color: #6c5cff; color: #f3f2ff; background: rgba(108,92,255,.12); }
  .tpl { text-align: left; padding: 12px; border-radius: 8px; background: #0d0f1c; border: 1px solid #232743; color: inherit; display: flex; flex-direction: column; gap: 4px; cursor: pointer; }
  .tpl.sel { border-color: #6c5cff; background: rgba(108,92,255,.08); }
  .tpl strong { font-size: .84rem; }
  .tpl small { color: #8f96bb; font-size: .74rem; line-height: 1.35; }
  .wiz-foot { display: flex; justify-content: center; }
  @media (max-width: 640px) { .stepper .lbl { display: none; } }
</style>
