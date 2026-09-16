<script>
  // GetStarted — describe what you want, watch it run.
  //
  // The dashboard has 25 pages and the shortest path to a working agent ran
  // through a visual graph editor. A first-time user met a wall of screens,
  // made a dozen decisions, and often never saw an agent produce anything at
  // all. The conversational builder that would have solved this existed on
  // the server for months with no screen calling it: its route id was even
  // aliased to Studio, so the one word that should have opened a conversation
  // opened the graph editor instead.
  //
  // The shape here is deliberate. One question. A few follow-ups in plain
  // language. A summary of what will happen, in words, not YAML. Then a real
  // run the user watches, because a deployed endpoint is a developer's idea of
  // success and an arriving result is everyone else's. Only after that is a
  // schedule offered, and only then is a cron armed — an agent that fires
  // unattended before anyone has seen it work is how a silent daily failure
  // gets created.
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { api, createEventSocket } from '../lib/api.js'

  // The app routes on the URL hash; there is no navigate helper to import.
  const go = (page) => { window.location.hash = page }

  // A greeting rather than a title. The screen is a conversation, and opening
  // with the product's own name would be the wrong voice for it.
  const hour = new Date().getHours()
  const partOfDay = hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening'

  // ask → converse → ready → running → result
  let phase = 'ask'
  let draft = ''
  let sessionId = ''
  let turns = []          // {role, text}
  let understanding = null
  let busy = false
  let error = ''

  let agentId = ''
  let output = ''
  let schedulePending = false
  let deliveryWarning = ''
  let scheduling = false
  let scheduled = false

  // Deliberately few, and phrased as outcomes rather than capabilities. A
  // blank box is the most expensive thing to put in front of a new user.
  const STARTERS = [
    { icon: '📰', chip: 'Daily',     title: 'Morning briefing', text: 'Every morning, summarise the news in my industry and send it to me.' },
    { icon: '📅', chip: 'Calendar',  title: 'Calendar watch',   text: 'Watch my calendar and warn me when two things clash.' },
    { icon: '📝', chip: 'Weekly',    title: 'Weekly summary',   text: 'Each Friday, write a short summary of what I worked on this week.' },
    { icon: '🔔', chip: 'Monitor',   title: 'Price watch',      text: 'Check a web page once a day and tell me when the price changes.' },
  ]

  // Setup, handled here rather than by sending someone away.
  //
  // A model has to exist before any of this can work, and the old banner just
  // pointed at another page — which is the same dead end in a nicer coat. If
  // nothing is usable we collect what is missing on this screen, then carry on
  // with whatever the person was already trying to do.
  let providerReady = true
  let checkingProvider = true
  let setupMode = ''          // '' | 'local' | 'cloud'
  let setupBusy = false
  let setupError = ''
  let localModels = []        // installable models that fit this machine
  let hostRamGB = 0
  let pullJob = null
  let pullTimer = null
  let cloudProvider = 'anthropic'
  let cloudKey = ''
  let cloudModels = []
  let cloudModel = ''
  // What the person asked for before we interrupted them, replayed after setup.
  let pendingMessage = ''

  const CLOUD_PROVIDERS = [
    { id: 'anthropic', label: 'Anthropic', where: 'console.anthropic.com' },
    { id: 'openai',    label: 'OpenAI',    where: 'platform.openai.com' },
    { id: 'google',    label: 'Google',    where: 'aistudio.google.com' },
  ]

  async function checkProvider() {
    checkingProvider = true
    try {
      const doctor = await api.providers.doctor()
      const providers = doctor.providers || []
      // No providers at all is also "not ready" — it used to read as fine.
      providerReady = providers.some(p => p.status === 'ok')
    } catch {
      providerReady = true   // cannot tell; do not block on a permissions error
    } finally {
      checkingProvider = false
    }
  }

  onMount(() => { checkProvider(); loadAgents() })
  // Real numbers only. The mockup shows a live agent rail; inventing health
  // percentages or a spend figure would be lying to the person reading it, so
  // this rail shows the agents that actually exist and what state they are in.
  let agents = []
  let agentsLoaded = false
  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = (res.agents || []).filter((a) => !['genie', 'system'].includes(a.id))
    } catch { agents = [] } finally { agentsLoaded = true }
  }
  $: runningCount = agents.filter((a) => a.enabled).length


  async function openLocalSetup() {
    setupMode = 'local'
    setupError = ''
    try {
      const res = await api.providers.suggested('ollama')
      hostRamGB = res.host_ram_gb || 0
      localModels = res.models || []
    } catch (e) {
      setupError = e.message || 'Could not reach a local model runtime on this machine.'
      localModels = []
    }
  }

  async function installModel(name) {
    setupBusy = true
    setupError = ''
    try {
      const res = await api.providers.pull('ollama', name)
      pullJob = { model: name, status: 'starting', completed: 0, total: 0, job: res.job_id }
      pollPull()
    } catch (e) {
      setupError = e.message || 'Could not start the download.'
      setupBusy = false
    }
  }

  function pollPull() {
    clearTimeout(pullTimer)
    pullTimer = setTimeout(async () => {
      if (!pullJob?.job) return
      try {
        const j = await api.providers.pullStatus('ollama', pullJob.job)
        pullJob = { ...pullJob, ...j }
        if (!j.done) { pollPull(); return }
        setupBusy = false
        if (j.error) { setupError = j.error; return }
        await api.providers.setModel('ollama', j.model)
        await finishSetup()
      } catch (e) {
        setupBusy = false
        setupError = e.message || 'Lost track of the download.'
      }
    }, 1000)
  }

  async function saveCloudKey() {
    if (!cloudKey.trim()) return
    setupBusy = true
    setupError = ''
    try {
      await api.providers.setCredentials(cloudProvider, { api_key: cloudKey.trim() })
      const res = await api.providers.models(cloudProvider)
      cloudModels = res.models || []
      cloudModel = res.selected || cloudModels[0] || ''
      cloudKey = ''   // not kept in the page once the gateway has it
    } catch (e) {
      setupError = e.message || 'That key was not accepted.'
    } finally {
      setupBusy = false
    }
  }

  async function useCloudModel() {
    if (!cloudModel) return
    setupBusy = true
    setupError = ''
    try {
      await api.providers.setModel(cloudProvider, cloudModel)
      await finishSetup()
    } catch (e) {
      setupError = e.message || 'Could not set that model.'
    } finally {
      setupBusy = false
    }
  }

  // Setup done: re-check, close the panel, and resume what they asked for.
  async function finishSetup() {
    await checkProvider()
    if (!providerReady) {
      setupError = 'Still not reachable. A gateway restart may be needed for a new provider.'
      return
    }
    setupMode = ''
    pullJob = null
    if (pendingMessage) {
      const msg = pendingMessage
      pendingMessage = ''
      send(msg)
    }
  }

  async function send(text) {
    const message = (text ?? draft).trim()
    if (!message || busy) return
    // Nothing can run without a model. Hold the request, set the model up on
    // this screen, then replay it — rather than failing three questions in
    // with a connection-refused error the person cannot act on.
    if (!providerReady && !setupMode) {
      pendingMessage = message
      draft = ''
      await openLocalSetup()
      return
    }
    busy = true
    error = ''
    turns = [...turns, { role: 'you', text: message }]
    draft = ''
    phase = 'converse'
    try {
      const res = await api.builder.chat(message, sessionId)
      sessionId = res.session_id || sessionId
      understanding = res.understanding || understanding
      if (res.reply) turns = [...turns, { role: 'soulacy', text: res.reply }]
      if (res.ready) phase = 'ready'
    } catch (e) {
      error = e.message || 'Could not reach the assistant builder.'
    } finally {
      busy = false
    }
  }

  // What the run is doing, while it does it.
  //
  // A first run takes ten to twenty seconds on a cloud model, and all the
  // screen said was "Running it for the first time". Silence that long reads
  // as a hang, and the person has no idea whether it is working or stuck. The
  // gateway already streams tool.call, reasoning.step and error events over a
  // socket; this listens for the ones belonging to the agent just created and
  // says, in words, what is happening.
  let runSteps = []
  let runSocket = null

  function humanTool(name) {
    const n = String(name || '').trim()
    if (!n) return 'a tool'
    // mcp__maverick-mcp__market_data_get_market_overview → market data get market overview
    const tail = n.includes('__') ? n.split('__').pop() : n
    return tail.replace(/[._]+/g, ' ').trim()
  }

  function watchRun(agentId) {
    stopWatching()
    try {
      runSocket = createEventSocket()
    } catch {
      runSocket = null
      return
    }
    runSocket.onmessage = (e) => {
      let ev
      try { ev = JSON.parse(e.data) } catch { return }
      if (ev.agent_id && ev.agent_id !== agentId) return
      const push = (text) => {
        // Collapse repeats: a model that calls the same tool twice should not
        // produce two identical lines the user has to read.
        if (runSteps[runSteps.length - 1]?.text === text) return
        runSteps = [...runSteps, { text, at: Date.now() }].slice(-6)
      }
      switch (ev.type) {
        case 'tool.call':    push('Using ' + humanTool(ev.payload?.name)); break
        case 'tool.result':  push('Reading the result'); break
        case 'reasoning.step': push('Working out the next step'); break
        case 'message.out':  push('Writing it up'); break
        case 'error':        push('Hit a problem, trying to recover'); break
      }
    }
    runSocket.onerror = () => stopWatching()
  }

  function stopWatching() {
    try { runSocket?.close() } catch { /* already gone */ }
    runSocket = null
  }

  onDestroy(stopWatching)

  // Create it, then run it once, in front of the user. The schedule is not
  // armed here; that is offered after they have seen the output.
  async function createAndRun() {
    busy = true
    error = ''
    phase = 'running'
    try {
      const dep = await api.builder.deploy(sessionId)
      agentId = dep.agent_id
      schedulePending = !!dep.schedule_pending
      deliveryWarning = dep.delivery_warning || ''
      runSteps = []
      watchRun(agentId)
      const run = await api.agents.trigger(agentId)
      stopWatching()
      // The manual-trigger endpoint returns `result`; the others return
      // `reply`. Accept both rather than depending on which one this is.
      output = run.result || run.reply || run.text || '(the agent produced no text this time)'
      phase = 'result'
    } catch (e) {
      stopWatching()
      error = e.message || 'Could not create the assistant.'
      phase = understanding ? 'ready' : 'converse'
    } finally {
      busy = false
    }
  }

  // Re-deploying the same session with the schedule armed. The agent id comes
  // from its name, so this updates the agent just created rather than making
  // a second one.
  async function activateSchedule() {
    scheduling = true
    error = ''
    try {
      const res = await api.builder.deploy(sessionId, '', '', true)
      scheduled = !!res.scheduled
      if (!scheduled) error = 'The schedule could not be started. You can set it up under Automations.'
      else schedulePending = false
    } catch (e) {
      error = e.message || 'Could not start the schedule.'
    } finally {
      scheduling = false
    }
  }

  function restart() {
    phase = 'ask'; draft = ''; sessionId = ''; turns = []; understanding = null
    agentId = ''; output = ''; schedulePending = false; deliveryWarning = ''
    scheduled = false; error = ''
  }

  // Plain sentences describing what the agent will do. The user is being asked
  // to approve behaviour, and a generated manifest is not something a
  // non-technical person can check.
  $: plan = (() => {
    if (!understanding) return []
    const out = []
    if (understanding.purpose) out.push(understanding.purpose)
    const t = understanding.trigger
    if (t?.type === 'cron' && t.schedule) out.push(`Runs on a schedule (${t.schedule}).`)
    else if (t?.type === 'channel') out.push('Runs when you message it.')
    else out.push('Runs when you ask it to.')
    if (understanding.tools?.length) {
      out.push('Uses: ' + understanding.tools.map(x => x.name).join(', ') + '.')
    }
    if (understanding.outputs?.length) {
      out.push('Sends results to: ' + understanding.outputs.map(o => o.channel).join(', ') + '.')
    }
    return out
  })()
</script>

<div class="console">
  <div class="console-main">
    <div class="eyebrow-row">
      <span class="eyebrow">WORKSPACE</span>
      {#if agentsLoaded && agents.length}
        <span class="pill"><span class="dot"></span>{runningCount} of {agents.length} agent{agents.length === 1 ? '' : 's'} running</span>
      {/if}
      <div class="head-actions"><TourButton /></div>
    </div>

    {#if phase === 'ask'}
      <h1 class="greeting">Good {partOfDay}. What would you like help with?</h1>

      {#if !checkingProvider && !providerReady && !setupMode}
        <div class="notice warn">No model is connected yet. Say what you want anyway and I will set one up first.</div>
      {/if}

      {#if setupMode}
        <div class="panel setup">
          <h2>First, a model to think with</h2>
          <p class="foot-note">
            {#if pendingMessage}Your request is saved. This takes a minute, then it carries on.{:else}Pick one and everything else follows.{/if}
          </p>
          <div class="tabs">
            <button class:on={setupMode === 'local'} on:click={openLocalSetup}>On this machine</button>
            <button class:on={setupMode === 'cloud'} on:click={() => { setupMode = 'cloud'; setupError = '' }}>Use a cloud account</button>
          </div>

          {#if setupMode === 'local'}
            {#if pullJob && !pullJob.done}
              <div class="pull">
                <div class="pull-row"><code>{pullJob.model}</code><span>{pullJob.status}</span></div>
                <div class="bar"><div class="fill" style={`width:${pullJob.total ? Math.round(pullJob.completed / pullJob.total * 100) : 4}%`}></div></div>
                <span class="foot-note">Downloading. You can leave this page; it keeps going.</span>
              </div>
            {:else if localModels.length}
              <p class="foot-note">{hostRamGB ? `${hostRamGB} GB of memory detected.` : ''} Anything too big for this machine is greyed out.</p>
              {#each localModels as m}
                <div class="row" class:unfit={!m.fits}>
                  <div><code>{m.name}</code>{#if m.default}<span class="chip ok">recommended</span>{/if}<div class="foot-note">{m.summary}</div></div>
                  <div class="row-side">
                    <span class="foot-note">{Math.round(m.size_gb * 10) / 10} GB</span>
                    {#if m.fits}
                      <button class="btn-secondary btn-sm" disabled={setupBusy} on:click={() => installModel(m.name)}>Install</button>
                    {:else}
                      <span class="foot-note">needs {m.min_ram_gb} GB</span>
                    {/if}
                  </div>
                </div>
              {/each}
            {:else}
              <p class="foot-note">No local model runtime is reachable on this machine. Use a cloud account instead.</p>
            {/if}
          {:else if setupMode === 'cloud'}
            {#if cloudModels.length}
              <label class="foot-note" for="cloud-model">Choose a model</label>
              <select id="cloud-model" bind:value={cloudModel}>{#each cloudModels as m}<option value={m}>{m}</option>{/each}</select>
              <button class="btn-primary btn-sm" disabled={!cloudModel || setupBusy} on:click={useCloudModel}>{setupBusy ? 'Saving…' : 'Use this model'}</button>
            {:else}
              <div class="tabs sub">
                {#each CLOUD_PROVIDERS as p}
                  <button class:on={cloudProvider === p.id} on:click={() => { cloudProvider = p.id; setupError = '' }}>{p.label}</button>
                {/each}
              </div>
              <p class="foot-note">Create a key at {CLOUD_PROVIDERS.find(p => p.id === cloudProvider)?.where}, then paste it here. It is stored encrypted on your gateway and never shown again.</p>
              <div class="key-row">
                <input type="password" placeholder="Paste the API key" bind:value={cloudKey} on:keydown={(e) => { if (e.key === 'Enter') saveCloudKey() }} />
                <button class="btn-secondary btn-sm" disabled={!cloudKey.trim() || setupBusy} on:click={saveCloudKey}>{setupBusy ? 'Checking…' : 'Save'}</button>
              </div>
            {/if}
          {/if}

          {#if setupError}<div class="notice err">{setupError}</div>{/if}
          <button class="btn-secondary btn-sm" on:click={() => { setupMode = ''; pendingMessage = '' }}>Not now</button>
        </div>
      {/if}

      <div class="intent-card">
        <div class="intent-head">
          <span class="intent-title">Describe what you want</span>
          <span class="intent-sub">Plain words. Soulacy works out the tools, shows you the plan, and runs it once before anything is scheduled.</span>
        </div>
        <textarea
          bind:value={draft}
          rows="3"
          placeholder="e.g. Every weekday at 7am, check my calendar and tell me what needs preparing."
          on:keydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send() }}
        ></textarea>
        <div class="intent-foot">
          <span class="kbd-hint">⌘ + Enter</span>
          <button class="btn-primary" disabled={!draft.trim() || busy} on:click={() => send()}>
            {busy ? 'Thinking…' : 'Continue'} <span class="arrow">→</span>
          </button>
        </div>
      </div>

      <div class="section-head">
        <h2>Start from one of these</h2>
        <button class="linkish" on:click={() => go('templates')}>All templates →</button>
      </div>
      <div class="cards">
        {#each STARTERS as s}
          <button class="card" on:click={() => send(s.text)}>
            <div class="card-top">
              <span class="tile">{s.icon}</span>
              <span class="chip">{s.chip}</span>
            </div>
            <span class="card-title">{s.title}</span>
            <span class="card-text">{s.text}</span>
          </button>
        {/each}
      </div>

      <div class="escape-row">
        <span>Want full control over tools, branching and code?</span>
        <button class="btn-secondary btn-sm" on:click={() => go('studio')}>Open Studio</button>
      </div>
    {:else}
      <div class="thread">
        {#each turns as t}
          <div class="msg {t.role}">
            <span class="who">{t.role === 'you' ? 'You' : 'Soulacy'}</span>
            <div class="bubble">{t.text}</div>
          </div>
        {/each}

        {#if phase === 'converse'}
          <div class="reply-card">
            <input
              bind:value={draft}
              placeholder="Answer, or add more detail…"
              on:keydown={(e) => { if (e.key === 'Enter') send() }}
            />
            <button class="btn-primary btn-sm" disabled={!draft.trim() || busy} on:click={() => send()}>
              {busy ? '…' : 'Send'}
            </button>
          </div>
          {#if busy}<div class="thinking"><span class="spinner"></span> Working it out…</div>{/if}
        {/if}

        {#if phase === 'ready'}
          <div class="panel">
            <h2>Here's what it will do</h2>
            <ul class="plan">{#each plan as line}<li>{line}</li>{/each}</ul>
            {#if understanding?.missing?.length}
              <div class="notice warn">Not covered: {understanding.missing.join(', ')}</div>
            {/if}
            <div class="panel-foot">
              <button class="btn-primary" disabled={busy} on:click={createAndRun}>Create it and run it now</button>
              <button class="btn-secondary btn-sm" on:click={() => { phase = 'converse' }}>Change something</button>
            </div>
            <p class="foot-note">It runs once, now, so you can see the result before deciding whether it should run on its own.</p>
          </div>
        {/if}

        {#if phase === 'running'}
          <div class="panel">
            <div class="run-head"><span class="spinner"></span> Running it for the first time…</div>
            {#if runSteps.length}
              <ul class="steps">
                {#each runSteps as st, i}
                  <li class:current={i === runSteps.length - 1}>{st.text}</li>
                {/each}
              </ul>
            {:else}
              <p class="foot-note">Starting it up.</p>
            {/if}
          </div>
        {/if}

        {#if phase === 'result'}
          <div class="panel">
            <h2>Here's what it produced</h2>
            <pre class="output">{output}</pre>
            {#if deliveryWarning}<div class="notice warn">{deliveryWarning}</div>{/if}
            {#if scheduled}
              <div class="notice ok">It will run on its own from now on.</div>
            {:else if schedulePending}
              <div class="offer">
                <span>Want this to happen automatically from now on?</span>
                <button class="btn-primary btn-sm" disabled={scheduling} on:click={activateSchedule}>
                  {scheduling ? 'Starting…' : 'Yes, run it on schedule'}
                </button>
              </div>
            {/if}
            <div class="panel-foot">
              <button class="btn-secondary btn-sm" on:click={() => go('agents')}>See it under Deployed</button>
              <button class="btn-secondary btn-sm" on:click={restart}>Build another</button>
            </div>
          </div>
        {/if}

        {#if error}<div class="notice err">{error}</div>{/if}
      </div>
    {/if}
  </div>

  {#if agentsLoaded && agents.length}
    <aside class="rail">
      <div class="rail-head">
        <h2>Your agents</h2>
        <span class="chip">{agents.length}</span>
      </div>
      {#each agents.slice(0, 6) as a}
        <button class="rail-card" on:click={() => go('agents')}>
          <div class="rail-top">
            <span class="rail-name">{a.name || a.id}</span>
            <span class="state {a.enabled ? 'on' : 'off'}">{a.enabled ? 'enabled' : 'off'}</span>
          </div>
          {#if a.description}<span class="rail-desc">{a.description}</span>{/if}
        </button>
      {/each}
    </aside>
  {/if}
</div>

<style>
  /* Built to the console layout: a wide main column with a live rail beside
     it, cards rather than lists, and one strong action per surface. The
     numbers in the rail are real — an invented health percentage would be a
     lie told in a nice font. */
  .console {
    display: grid; grid-template-columns: minmax(0, 1fr) 280px; gap: 1.6rem;
    max-width: 1180px; margin: 0 auto; padding: 1.6rem 1.25rem 3rem;
  }
  @media (max-width: 1000px) { .console { grid-template-columns: 1fr; } .rail { order: -1; } }

  .eyebrow-row { display: flex; align-items: center; gap: .6rem; margin-bottom: .9rem; }
  .eyebrow { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .64rem; letter-spacing: .14em; text-transform: uppercase; }
  .head-actions { margin-left: auto; }
  .pill {
    display: inline-flex; align-items: center; gap: .35rem;
    background: var(--sl-surface, var(--sl-surface)); border: 1px solid var(--sl-line, #1f2440);
    color: var(--sl-text-dim, var(--sl-text-dim)); border-radius: 999px; padding: .2rem .6rem; font-size: .7rem;
  }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: #4caf82; }

  .greeting { font-size: 2.15rem; line-height: 1.18; font-weight: 600; letter-spacing: -.015em; margin-bottom: 1.1rem; max-width: 22ch; }

  /* The intent card is the one thing on the page that matters. */
  .intent-card {
    background: var(--sl-surface, var(--sl-surface)); border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius-lg, 14px); padding: 1.1rem 1.15rem; margin-bottom: 1.6rem;
  }
  .intent-head { display: flex; flex-direction: column; gap: .15rem; margin-bottom: .7rem; }
  .intent-title { font-size: .9rem; font-weight: 600; }
  .intent-sub { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .78rem; line-height: 1.5; }
  .intent-card textarea {
    width: 100%; background: #0f111c; border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius, 10px); padding: .85rem .95rem; color: inherit;
    font: inherit; font-size: .92rem; resize: vertical;
    transition: border-color .15s ease, box-shadow .15s ease;
  }
  .intent-card textarea:focus {
    outline: none; border-color: var(--sl-accent, var(--sl-accent));
    box-shadow: 0 0 0 3px var(--sl-accent-soft, rgba(139,133,255,.12));
  }
  .intent-foot { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-top: .75rem; }
  .kbd-hint { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .7rem; }
  .arrow { margin-left: .2rem; }

  .section-head { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: .7rem; }
  .section-head h2 { font-size: .95rem; font-weight: 600; }

  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: .7rem; }
  .card {
    display: flex; flex-direction: column; gap: .35rem; text-align: left; cursor: pointer;
    background: var(--sl-surface, var(--sl-surface)); border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius-lg, 14px); padding: .95rem 1rem;
    transition: border-color .15s ease, background .15s ease, transform .1s ease;
  }
  .card:hover { border-color: var(--sl-accent, var(--sl-accent)); background: var(--sl-surface-raised, #191c2f); }
  .card:active { transform: translateY(1px); }
  .card-top { display: flex; align-items: center; justify-content: space-between; margin-bottom: .2rem; }
  .tile {
    width: 30px; height: 30px; display: grid; place-items: center; font-size: .95rem;
    background: var(--sl-accent-soft, rgba(139,133,255,.12)); border-radius: 9px;
  }
  .chip {
    background: #11131f; border: 1px solid var(--sl-line, #1f2440); color: var(--sl-text-faint, var(--sl-text-faint));
    border-radius: 6px; padding: .1rem .4rem; font-size: .62rem; text-transform: uppercase; letter-spacing: .06em;
  }
  .chip.ok { background: rgba(76,175,130,.16); border-color: transparent; color: #4caf82; text-transform: none; letter-spacing: 0; }
  .card-title { font-size: .88rem; font-weight: 500; color: var(--sl-text, var(--sl-text)); }
  .card-text { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .78rem; line-height: 1.5; }

  .escape-row { display: flex; align-items: center; gap: .7rem; margin-top: 1.6rem; color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .78rem; flex-wrap: wrap; }

  /* The follow-up reads as the same product: same card, same spacing. */
  .thread { display: flex; flex-direction: column; gap: 1rem; }
  .msg .who { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .62rem; text-transform: uppercase; letter-spacing: .1em; }
  .bubble { margin-top: .25rem; font-size: .93rem; line-height: 1.6; }
  .msg.you .bubble { color: var(--sl-text, var(--sl-text)); font-weight: 500; }
  .msg.soulacy .bubble {
    color: var(--sl-text-dim, var(--sl-text-dim)); background: var(--sl-surface, var(--sl-surface));
    border: 1px solid var(--sl-line, #1f2440); border-radius: var(--sl-radius-lg, 14px);
    padding: .8rem .95rem;
  }

  .reply-card { display: flex; gap: .5rem; }
  .reply-card input {
    flex: 1; background: #0f111c; border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius, 10px); padding: .7rem .85rem; color: inherit; font: inherit; font-size: .9rem;
  }
  .reply-card input:focus { outline: none; border-color: var(--sl-accent, var(--sl-accent)); }
  .thinking { display: flex; align-items: center; gap: .5rem; color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .8rem; }

  .panel {
    background: var(--sl-surface, var(--sl-surface)); border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius-lg, 14px); padding: 1.1rem 1.15rem;
  }
  .panel h2 { font-size: .98rem; font-weight: 600; margin-bottom: .55rem; }
  .run-head { display: flex; align-items: center; gap: .6rem; color: var(--sl-text-dim, var(--sl-text-dim)); font-size: .9rem; }
  .steps { list-style: none; margin: .7rem 0 0; padding: 0; display: flex; flex-direction: column; gap: .3rem; }
  .steps li {
    color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .8rem; padding-left: 1rem; position: relative;
  }
  .steps li::before { content: '·'; position: absolute; left: .3rem; }
  /* The newest line is the one that is happening now. */
  .steps li.current { color: var(--sl-text, var(--sl-text)); }
  .steps li.current::before { content: '→'; left: .1rem; }

  .panel.running { display: flex; align-items: center; gap: .6rem; color: var(--sl-text-dim, var(--sl-text-dim)); font-size: .9rem; }
  .plan { margin: 0 0 .5rem 1.05rem; }
  .plan li { font-size: .89rem; line-height: 1.65; color: var(--sl-text, var(--sl-text)); }
  .panel-foot { display: flex; align-items: center; gap: .9rem; margin-top: .8rem; flex-wrap: wrap; }
  .foot-note { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .76rem; line-height: 1.5; margin-top: .4rem; }
  .output {
    background: #0f111c; border: 1px solid var(--sl-line, #1f2440); border-radius: var(--sl-radius, 10px);
    padding: .9rem 1rem; white-space: pre-wrap; word-break: break-word;
    font-size: .85rem; line-height: 1.6; color: var(--sl-text, var(--sl-text)); max-height: 420px; overflow: auto;
  }
  .offer {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap;
    margin-top: .8rem; padding: .7rem .9rem; border: 1px solid var(--sl-line, #1f2440);
    border-radius: var(--sl-radius, 10px); background: #0f111c; font-size: .88rem;
  }

  .notice { padding: .65rem .85rem; border-radius: var(--sl-radius, 10px); font-size: .82rem; margin-top: .7rem; }
  .warn { background: rgba(232,168,72,.1); border: 1px solid rgba(232,168,72,.32); color: #e8a848; }
  .ok   { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .setup .tabs { display: flex; gap: .35rem; margin: .6rem 0; flex-wrap: wrap; }
  .setup .tabs button {
    background: #0f111c; border: 1px solid var(--sl-line, #1f2440); color: var(--sl-text-dim, var(--sl-text-dim));
    border-radius: 8px; padding: .35rem .7rem; font-size: .78rem; cursor: pointer;
  }
  .setup .tabs button.on { border-color: var(--sl-accent, var(--sl-accent)); color: var(--sl-text, var(--sl-text)); }
  .setup select, .key-row input {
    background: #0f111c; border: 1px solid var(--sl-line, #1f2440); border-radius: 8px;
    padding: .45rem .6rem; color: inherit; font-size: .82rem;
  }
  .setup select { width: 100%; }
  .key-row { display: flex; gap: .4rem; }
  .key-row input { flex: 1; }
  .row {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
    padding: .5rem .65rem; border: 1px solid var(--sl-line, #1f2440); border-radius: 9px;
    background: #0f111c; margin-bottom: .3rem;
  }
  .row.unfit { opacity: .55; }
  .row code { color: #8b85ff; font-size: .82rem; }
  .row-side { display: flex; align-items: center; gap: .5rem; flex-shrink: 0; }
  .pull-row { display: flex; justify-content: space-between; font-size: .8rem; }
  .pull-row code { color: #8b85ff; }
  .bar { height: 5px; background: var(--sl-line); border-radius: 3px; overflow: hidden; margin: .4rem 0 .3rem; }
  .fill { height: 100%; background: var(--sl-accent, var(--sl-accent)); transition: width .3s ease; }

  /* The rail: what you actually have, not a telemetry feed. */
  .rail { display: flex; flex-direction: column; gap: .5rem; }
  .rail-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: .2rem; }
  .rail-head h2 { font-size: .85rem; font-weight: 600; }
  .rail-card {
    text-align: left; cursor: pointer; background: var(--sl-surface, var(--sl-surface));
    border: 1px solid var(--sl-line, #1f2440); border-radius: var(--sl-radius, 10px);
    padding: .65rem .75rem; display: flex; flex-direction: column; gap: .2rem;
    transition: border-color .15s ease;
  }
  .rail-card:hover { border-color: var(--sl-accent, var(--sl-accent)); }
  .rail-top { display: flex; align-items: center; justify-content: space-between; gap: .5rem; }
  .rail-name { font-size: .82rem; color: var(--sl-text, var(--sl-text)); }
  .state { font-size: .62rem; border-radius: 5px; padding: .08rem .35rem; }
  .state.on { background: rgba(76,175,130,.16); color: #4caf82; }
  .state.off { background: #11131f; color: var(--sl-text-faint, var(--sl-text-faint)); }
  .rail-desc { color: var(--sl-text-faint, var(--sl-text-faint)); font-size: .72rem; line-height: 1.45;
               display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }

  .spinner {
    width: 14px; height: 14px; border: 2px solid var(--sl-line); border-top-color: var(--sl-accent, var(--sl-accent));
    border-radius: 50%; animation: spin .8s linear infinite; display: inline-block;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
