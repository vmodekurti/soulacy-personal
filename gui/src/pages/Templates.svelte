<script>
  import TourButton from '../lib/TourButton.svelte'
  // Templates tab (Story E21): the template catalog as a first-class page —
  // four default agentic workflows ship embedded (Meeting Minutes, Inbox
  // Triage, Market Monitor, Compliance Auditor) alongside the starter
  // templates; user-dir templates appear with a "user" badge. One click
  // creates a ready-to-run agent.
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let templates = []
  let channels = []
  let loading   = true
  let channelsLoading = false
  let error     = ''
  let notice    = ''
  let instantiating = ''
  let copiedPrompt = ''
  let wizard = null
  let wizardId = ''
  let wizardCron = ''
  let wizardChannel = ''
  let wizardTo = ''
  let wizardTemplate = '{reply}'
  let createdAgent = null
  let testingOutput = false
  let readiness = null       // { ready, checks[] }
  let readinessBusy = false
  let mockResult = null      // { plan[], note }
  let mockBusy = false

  const WORKFLOW_TAG = 'workflow'

  async function load() {
    loading = true
    channelsLoading = true
    error   = ''
    try {
      const [tplRes, chRes] = await Promise.allSettled([
        api.templates.list(),
        api.channels.list(),
      ])
      if (tplRes.status === 'rejected') throw tplRes.reason
      templates = tplRes.value.templates || []
      if (chRes.status === 'fulfilled') channels = chRes.value.channels || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
      channelsLoading = false
    }
  }

  function slugify(s) {
    return String(s || '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 48)
  }

  function openWizard(t) {
    wizard = t
    wizardId = slugify(t.display_name || t.name) || ''
    wizardCron = t.definition?.schedule?.cron || ''
    wizardChannel = t.definition?.schedule?.output?.channel || ''
    wizardTo = t.definition?.schedule?.output?.to || ''
    wizardTemplate = t.definition?.schedule?.output?.template || '{reply}'
    createdAgent = null
    readiness = null
    mockResult = null
    error = ''
    notice = ''
    if (!channels.length) loadChannels()
    checkReadiness(t)
  }

  async function loadChannels() {
    channelsLoading = true
    try {
      const res = await api.channels.list()
      channels = res.channels || []
    } catch (_) {
      channels = []
    } finally {
      channelsLoading = false
    }
  }

  async function checkReadiness(t = wizard) {
    if (!t) return
    readinessBusy = true
    try {
      readiness = await api.templates.readiness(t.name)
    } catch (e) {
      readiness = null
    } finally {
      readinessBusy = false
    }
  }

  async function runMockTest(t = wizard) {
    if (!t) return
    mockBusy = true
    error = ''
    try {
      mockResult = await api.templates.mockTest(t.name)
    } catch (e) {
      error = e.message || 'Mock test failed'
    } finally {
      mockBusy = false
    }
  }

  async function useTemplate(t = wizard) {
    if (!t) return
    instantiating = t.name
    error  = ''
    notice = ''
    try {
      const payload = { id: wizardId.trim() }
      if (wizardCron.trim()) payload.cron = wizardCron.trim()
      if (wizardChannel.trim() || wizardTo.trim()) {
        payload.output = {
          channel: wizardChannel.trim(),
          to: wizardTo.trim(),
          template: wizardTemplate.trim() || '{reply}',
        }
      }
      const def = await api.templates.instantiate(t.name, payload)
      createdAgent = def
      notice = `Agent "${def.id}" created from "${t.display_name || t.name}".`
    } catch (e) {
      error = e.message
    } finally {
      instantiating = ''
    }
  }

  async function testOutput() {
    if (!createdAgent?.id) return
    testingOutput = true
    error = ''
    try {
      await api.agents.testScheduleOutput(createdAgent.id)
      notice = `Sent a test output for "${createdAgent.id}".`
    } catch (e) {
      error = e.message || 'Test output failed'
    } finally {
      testingOutput = false
    }
  }

  async function copyPrompt(t) {
    const prompt = t.mock_prompt || 'Say hello and explain what you can do.'
    error = ''
    try {
      await navigator.clipboard.writeText(prompt)
      copiedPrompt = t.name
      notice = `Copied a test prompt for "${t.display_name || t.name}".`
      setTimeout(() => {
        if (copiedPrompt === t.name) copiedPrompt = ''
      }, 2200)
    } catch (e) {
      error = 'Could not copy the test prompt.'
    }
  }

  function statusLabel(status) {
    if (status === 'needs_setup') return 'Setup'
    if (status === 'optional') return 'Optional'
    return 'Ready'
  }

  function blockers(t) {
    return (t.setup || []).filter(item => item.status === 'needs_setup')
  }

  function hasSchedule(t) {
    return !!(t.definition?.schedule || t.schedule_hint || (t.setup || []).find(x => x.key === 'schedule'))
  }

  function isTruthy(value) {
    return value === true || String(value).toLowerCase() === 'true'
  }

  function hasValue(value) {
    return String(value ?? '').trim() !== '' && String(value ?? '').trim() !== '—' && String(value ?? '').trim() !== '-'
  }

  function channelOptionLabel(channel, row) {
    const base = row.bot_name || channel.name || channel.id
    const mode = row.outbound_only ? 'scheduled output' : (row.agent_id ? `agent ${row.agent_id}` : 'delivery')
    return `${base} · ${row.adapter_id} · ${mode}`
  }

  function channelOptionsFrom(channelsList) {
    const out = []
    for (const ch of channelsList || []) {
      const defaultDest = ch.settings?.default_output_to || ''
      const defaultTemplate = ch.settings?.default_output_template || ''
      const hasDefault = ch.configured || hasValue(defaultDest) || ch.status?.connected
      if (hasDefault) {
        out.push({
          adapter_id: ch.id,
          label: channelOptionLabel(ch, {
            adapter_id: ch.id,
            bot_name: ch.settings?.bot_name || ch.name || ch.id,
            agent_id: ch.settings?.agent_id || '',
            outbound_only: isTruthy(ch.settings?.outbound_only) || !ch.settings?.agent_id,
          }),
          destination: defaultDest,
          template: defaultTemplate,
          connected: !!ch.status?.connected,
          detail: ch.status?.detail || '',
        })
      }
      for (const bot of ch.bots || []) {
        out.push({
          adapter_id: bot._adapter_id,
          label: channelOptionLabel(ch, {
            adapter_id: bot._adapter_id,
            bot_name: bot.bot_name || bot.name || ch.name || ch.id,
            agent_id: bot.agent_id || '',
            outbound_only: isTruthy(bot.outbound_only),
          }),
          destination: bot.default_output_to || '',
          template: bot.default_output_template || '',
          connected: !!bot._connected,
          detail: bot._detail || '',
        })
      }
    }
    return out.filter(x => hasValue(x.adapter_id))
  }

  function applyChannelOption() {
    const opt = outputOptions.find(x => x.adapter_id === wizardChannel)
    if (!opt) return
    if (!wizardTo.trim() && hasValue(opt.destination)) wizardTo = String(opt.destination)
    if ((!wizardTemplate.trim() || wizardTemplate.trim() === '{reply}') && hasValue(opt.template)) {
      wizardTemplate = String(opt.template)
    }
  }

  $: workflows = templates.filter(t => (t.tags || []).includes(WORKFLOW_TAG))
  $: starters  = templates.filter(t => !(t.tags || []).includes(WORKFLOW_TAG))
  $: outputOptions = channelOptionsFrom(channels)
  $: selectedOutputOption = outputOptions.find(x => x.adapter_id === wizardChannel)

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Templates</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
        <TourButton />
    </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}
  {#if notice}<div class="banner ok">✓ {notice}</div>{/if}

  {#if loading}
    <p class="hint">Loading templates…</p>
  {:else if templates.length === 0}
    <p class="hint">
      No templates available. Defaults ship with the gateway; drop extra
      <code>*.yaml</code> agent definitions in <code>~/.soulacy/templates</code>
      to add your own.
    </p>
  {:else}
    {#if workflows.length > 0}
      <h2 class="section-title">Agentic workflows</h2>
      <p class="hint">Ready-made multi-step workflows — create the agent, follow the setup note in its description, go.</p>
      <div class="grid">
        {#each workflows as t (t.name)}
          <div class="card tpl">
            <div class="tpl-head">
              <h3>{t.display_name || t.name}</h3>
              <span class="badge {t.source}">{t.source}</span>
            </div>
            <p class="desc">{t.description}</p>
            {#if t.tags?.length}
              <div class="tags">{#each t.tags.filter(x => x !== 'template') as tag}<span class="tag">{tag}</span>{/each}</div>
            {/if}
            <div class="readiness">
              {#each t.setup || [] as item}
                <span class="check {item.status}" title={item.detail}>{item.label}: {statusLabel(item.status)}</span>
              {/each}
            </div>
            {#if t.required_secrets?.length || blockers(t).length}
              <div class="setup-panel">
                {#if t.required_secrets?.length}
                  <div class="setup-title">Required secrets</div>
                  {#each t.required_secrets as secret}
                    <div class="setup-row">{secret.label}<span>{secret.key}</span></div>
                  {/each}
                {/if}
                {#if blockers(t).length}
                  <div class="setup-title">Before production</div>
                  {#each blockers(t) as item}
                    <div class="setup-row">{item.label}<span>{item.detail}</span></div>
                  {/each}
                {/if}
              </div>
            {/if}
            {#if t.mock_prompt}
              <div class="mock">
                <div class="mock-label">Test prompt</div>
                <p>{t.mock_prompt}</p>
              </div>
            {/if}
            <div class="actions">
              <button class="btn-secondary" on:click={() => copyPrompt(t)}>
                {copiedPrompt === t.name ? 'Copied' : 'Copy test prompt'}
              </button>
              <button class="btn-primary" on:click={() => openWizard(t)} disabled={instantiating === t.name}>
                Install
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}

    {#if starters.length > 0}
      <h2 class="section-title">Starters</h2>
      <div class="grid">
        {#each starters as t (t.name)}
          <div class="card tpl">
            <div class="tpl-head">
              <h3>{t.display_name || t.name}</h3>
              <span class="badge {t.source}">{t.source}</span>
            </div>
            <p class="desc">{t.description}</p>
            {#if t.tags?.length}
              <div class="tags">{#each t.tags.filter(x => x !== 'template') as tag}<span class="tag">{tag}</span>{/each}</div>
            {/if}
            <div class="readiness">
              {#each t.setup || [] as item}
                <span class="check {item.status}" title={item.detail}>{item.label}: {statusLabel(item.status)}</span>
              {/each}
            </div>
            {#if t.required_secrets?.length || blockers(t).length}
              <div class="setup-panel">
                {#if t.required_secrets?.length}
                  <div class="setup-title">Required secrets</div>
                  {#each t.required_secrets as secret}
                    <div class="setup-row">{secret.label}<span>{secret.key}</span></div>
                  {/each}
                {/if}
                {#if blockers(t).length}
                  <div class="setup-title">Before production</div>
                  {#each blockers(t) as item}
                    <div class="setup-row">{item.label}<span>{item.detail}</span></div>
                  {/each}
                {/if}
              </div>
            {/if}
            {#if t.mock_prompt}
              <div class="mock">
                <div class="mock-label">Test prompt</div>
                <p>{t.mock_prompt}</p>
              </div>
            {/if}
            <div class="actions">
              <button class="btn-secondary" on:click={() => copyPrompt(t)}>
                {copiedPrompt === t.name ? 'Copied' : 'Copy test prompt'}
              </button>
              <button class="btn-primary" on:click={() => openWizard(t)} disabled={instantiating === t.name}>
                Install
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

{#if wizard}
  <div class="modal-bg" role="button" tabindex="0" aria-label="Close install wizard"
       on:click|self={() => wizard = null}
       on:keydown={(e) => e.key === 'Escape' && (wizard = null)}>
    <div class="wizard">
      <div class="wizard-head">
        <div>
          <span class="wizard-eyebrow">Template install</span>
          <h2>{wizard.display_name || wizard.name}</h2>
        </div>
        <button class="ghost" on:click={() => wizard = null}>×</button>
      </div>

      <p class="wizard-desc">{wizard.description}</p>

      <label>
        Agent ID
        <input bind:value={wizardId} placeholder="daily-briefing" disabled={!!createdAgent} />
      </label>

      <div class="wizard-grid">
        <div>
          <h3>Readiness {#if readiness}<span class="ready-pill {readiness.ready ? 'ok' : 'no'}">{readiness.ready ? 'ready' : 'needs setup'}</span>{/if}
            <button class="btn-link" on:click={() => checkReadiness(wizard)} disabled={readinessBusy}>{readinessBusy ? '…' : 'recheck'}</button>
          </h3>
          <div class="wizard-list">
            {#if readiness && readiness.checks}
              {#each readiness.checks as ck}
                <div class="wizard-row {ck.satisfied ? 'ready' : (ck.optional ? 'optional' : 'needs_setup')}">
                  <strong>{ck.satisfied ? '✓' : (ck.optional ? '○' : '✗')} {ck.label}</strong>
                  <span>{ck.detail}</span>
                </div>
              {/each}
            {:else}
              {#each wizard.setup || [] as item}
                <div class="wizard-row {item.status}">
                  <strong>{item.label}</strong>
                  <span>{item.detail}</span>
                </div>
              {/each}
            {/if}
          </div>
        </div>
        <div>
          <h3>Production options</h3>
          <label>
            Cron
            <input bind:value={wizardCron} placeholder="0 7 * * *" disabled={!!createdAgent} />
          </label>
          <label>
            Output channel
            <select bind:value={wizardChannel} on:change={applyChannelOption} disabled={!!createdAgent}>
              <option value="">Do not send scheduled output</option>
              {#if wizardChannel && !selectedOutputOption}
                <option value={wizardChannel}>{wizardChannel} · from template</option>
              {/if}
              {#each outputOptions as opt}
                <option value={opt.adapter_id}>{opt.label}</option>
              {/each}
            </select>
          </label>
          {#if wizardChannel && selectedOutputOption}
            <div class="delivery-hint {selectedOutputOption.connected ? 'ok' : 'warn'}">
              {selectedOutputOption.connected ? 'Connected' : 'Needs restart or credentials'}
              {#if selectedOutputOption.detail} · {selectedOutputOption.detail}{/if}
            </div>
          {:else if !outputOptions.length}
            <div class="delivery-hint warn">
              {channelsLoading ? 'Loading delivery channels…' : 'No configured output bots found. Open Channels to add Telegram, Slack, Discord, WhatsApp, or a webhook.'}
              <button class="btn-link" type="button" on:click={() => window.location.hash = '#channels'}>Open Channels</button>
            </div>
          {/if}
          <label>
            Destination
            <input bind:value={wizardTo} placeholder="@channel, chat ID, Slack channel, Discord channel, or webhook URL" disabled={!!createdAgent || !wizardChannel} />
          </label>
          <label>
            Output template
            <textarea bind:value={wizardTemplate} rows="3" disabled={!!createdAgent}></textarea>
          </label>
        </div>
      </div>

      {#if wizard.required_secrets?.length}
        <div class="secrets-note">
          <strong>Secrets needed</strong>
          {#each wizard.required_secrets as secret}
            <span>{secret.label}: {secret.key}</span>
          {/each}
        </div>
      {/if}

      {#if mockResult}
        <div class="mock">
          <div class="mock-label">Mock test — {mockResult.note}</div>
          <ul class="mock-plan">
            {#each mockResult.plan || [] as step}<li>{step}</li>{/each}
          </ul>
        </div>
      {:else if wizard.mock_prompt}
        <div class="mock">
          <div class="mock-label">Smoke test prompt</div>
          <p>{wizard.mock_prompt}</p>
        </div>
      {/if}

      <div class="wizard-actions">
        {#if createdAgent}
          <button class="btn-secondary" on:click={() => window.location.hash = '#agents'}>Open Deployed</button>
          <button class="btn-secondary" on:click={() => window.location.hash = '#chat'}>Try in Chat</button>
          <button class="btn-secondary" on:click={() => api.agents.trigger(createdAgent.id).then(() => notice = 'Real test run started for "' + createdAgent.id + '".').catch(e => error = e.message)}>
            Run real test
          </button>
          {#if hasSchedule(wizard)}
            <button class="btn-primary" on:click={testOutput} disabled={testingOutput}>
              {testingOutput ? 'Sending…' : 'Test output'}
            </button>
          {/if}
        {:else}
          <button class="btn-secondary" on:click={() => wizard = null}>Cancel</button>
          <button class="btn-secondary" on:click={() => runMockTest(wizard)} disabled={mockBusy}>
            {mockBusy ? 'Testing…' : 'Mock test'}
          </button>
          <button class="btn-primary" on:click={() => useTemplate(wizard)} disabled={instantiating === wizard.name || !wizardId.trim()}>
            {instantiating === wizard.name ? 'Installing…' : 'Install agent'}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .page { display: flex; flex-direction: column; gap: 14px; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .section-title { font-size: 1rem; margin: .4rem 0 0; color: #c8cbe8; }
  .hint { font-size: .8rem; color: #6b7294; }
  .hint code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; }
  .banner { padding: .55rem .8rem; border-radius: 8px; font-size: .85rem; }
  .banner.err { background: rgba(240, 96, 96, .12); color: #f08080; }
  .banner.ok { background: rgba(76, 175, 130, .12); color: #5fce9a; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
  .card.tpl { display: flex; flex-direction: column; gap: .5rem; padding: .9rem 1rem;
              background: #10121f; border: 1px solid #1a1e36; border-radius: 10px; }
  .tpl-head { display: flex; align-items: baseline; justify-content: space-between; gap: .5rem; }
  .tpl-head h3 { margin: 0; font-size: .95rem; }
  .badge { font-size: .65rem; padding: .1rem .45rem; border-radius: 999px; text-transform: uppercase; }
  .badge.embedded { background: rgba(139, 133, 255, .15); color: #8b85ff; }
  .badge.user { background: rgba(240, 160, 96, .15); color: #f0a060; }
  .desc { font-size: .78rem; color: #9aa0c3; line-height: 1.5; white-space: pre-line; flex: 1; }
  .tags { display: flex; flex-wrap: wrap; gap: .3rem; }
  .tag { font-size: .65rem; background: #1c1f35; color: #6b7294; padding: .1rem .45rem; border-radius: 999px; }
  .readiness { display: flex; flex-wrap: wrap; gap: .3rem; }
  .check { font-size: .65rem; border: 1px solid #252a46; background: #15182a; color: #aeb4d7; padding: .12rem .45rem; border-radius: 999px; }
  .check.ready { border-color: rgba(95, 206, 154, .28); color: #72d9aa; background: rgba(76, 175, 130, .08); }
  .check.needs_setup { border-color: rgba(240, 160, 96, .32); color: #f0b070; background: rgba(240, 160, 96, .08); }
  .check.optional { border-color: rgba(139, 133, 255, .28); color: #aaa6ff; background: rgba(139, 133, 255, .08); }
  .setup-panel { display: flex; flex-direction: column; gap: .35rem; border-top: 1px solid #20243d; padding-top: .55rem; }
  .setup-title { font-size: .66rem; color: #6f769a; text-transform: uppercase; letter-spacing: .04em; }
  .setup-row { display: flex; align-items: baseline; justify-content: space-between; gap: .65rem; font-size: .72rem; color: #c2c6e2; }
  .setup-row span { color: #7b83a8; text-align: right; max-width: 58%; }
  .mock { border: 1px solid #20243d; background: #0d1020; border-radius: 8px; padding: .55rem .65rem; }
  .mock-label { font-size: .66rem; color: #777fa3; text-transform: uppercase; letter-spacing: .04em; margin-bottom: .25rem; }
  .mock p { margin: 0; font-size: .75rem; color: #b9bedc; line-height: 1.45; }
  .actions { display: flex; flex-wrap: wrap; gap: .45rem; margin-top: auto; }
  .actions button { align-self: flex-start; }
  .modal-bg { position: fixed; inset: 0; background: rgba(3, 5, 12, .72); display: grid; place-items: center; padding: 18px; z-index: 50; }
  .wizard { width: min(760px, 100%); max-height: min(90vh, 860px); overflow: auto; background: #111426; border: 1px solid #2a2f4a; border-radius: 10px; padding: 18px; box-shadow: 0 24px 80px rgba(0, 0, 0, .45); display: flex; flex-direction: column; gap: 14px; }
  .wizard-head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; }
  .wizard-head h2 { margin: .12rem 0 0; font-size: 1.05rem; }
  .wizard-eyebrow { color: #8b85ff; text-transform: uppercase; letter-spacing: .08em; font-size: .68rem; }
  .wizard-desc { color: #aeb4d7; font-size: .82rem; line-height: 1.5; white-space: pre-line; }
  .ghost { background: transparent; color: #aeb4d7; font-size: 1.2rem; padding: .2rem .45rem; }
  label { display: flex; flex-direction: column; gap: .35rem; color: #c8cbe8; font-size: .75rem; }
  .wizard-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .wizard-grid h3 { margin: 0 0 .5rem; font-size: .82rem; color: #c8cbe8; }
  select {
    background: #171b30;
    color: #d8dcff;
    border: 1px solid #2a2f4a;
    border-radius: 7px;
    padding: .55rem .6rem;
    font: inherit;
    min-height: 2.2rem;
  }
  .delivery-hint {
    border-radius: 8px;
    padding: .45rem .55rem;
    font-size: .72rem;
    line-height: 1.45;
  }
  .delivery-hint.ok { color: #72d9aa; background: rgba(76,175,130,.08); border: 1px solid rgba(95,206,154,.22); }
  .delivery-hint.warn { color: #f0b070; background: rgba(240,160,96,.08); border: 1px solid rgba(240,160,96,.24); }
  .wizard-list { display: flex; flex-direction: column; gap: .45rem; }
  .wizard-row { border: 1px solid #252a46; background: #15182a; border-radius: 8px; padding: .55rem .65rem; }
  .wizard-row.ready { border-color: rgba(95, 206, 154, .25); }
  .wizard-row.needs_setup { border-color: rgba(240, 160, 96, .3); }
  .wizard-row.optional { border-color: #252a46; opacity: .82; }
  .ready-pill { font-size: .6rem; padding: .05rem .4rem; border-radius: 999px; text-transform: uppercase; letter-spacing: .04em; vertical-align: middle; }
  .ready-pill.ok { background: rgba(95,206,154,.18); color: #5fce9a; }
  .ready-pill.no { background: rgba(240,160,96,.18); color: #f0a060; }
  .btn-link { background: none; border: none; color: #8b85ff; font-size: .68rem; cursor: pointer; margin-left: .4rem; }
  .mock-plan { margin: .3rem 0 0; padding-left: 1.1rem; font-size: .74rem; color: #b9bedc; line-height: 1.5; }
  .wizard-row strong, .wizard-row span { display: block; }
  .wizard-row strong { font-size: .75rem; }
  .wizard-row span { color: #8f96bb; font-size: .72rem; margin-top: .12rem; }
  .secrets-note { border: 1px solid rgba(240, 160, 96, .28); background: rgba(240, 160, 96, .07); border-radius: 8px; padding: .65rem; display: flex; flex-direction: column; gap: .25rem; font-size: .75rem; }
  .secrets-note span { color: #f0b070; }
  .wizard-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: .5rem; }
  @media (max-width: 760px) {
    .wizard-grid { grid-template-columns: 1fr; }
  }
</style>
