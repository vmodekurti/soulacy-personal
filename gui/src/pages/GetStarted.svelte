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
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  // The app routes on the URL hash; there is no navigate helper to import.
  const go = (page) => { window.location.hash = page }

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
    'Every morning, summarise the news in my industry and send it to me.',
    'Watch my calendar and warn me when two things clash.',
    'Each Friday, write a short summary of what I worked on this week.',
    'Check a web page once a day and tell me when the price changes.',
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

  onMount(checkProvider)

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
      const run = await api.agents.trigger(agentId)
      // The manual-trigger endpoint returns `result`; the others return
      // `reply`. Accept both rather than depending on which one this is.
      output = run.result || run.reply || run.text || '(the agent produced no text this time)'
      phase = 'result'
    } catch (e) {
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

<div class="page">
  <div class="page-head"><TourButton /></div>
  {#if phase === 'ask'}
    <div class="hero">
      <h1>What would you like help with?</h1>
      <p class="sub">Describe it in your own words. Soulacy works out the rest and shows you before anything runs.</p>

      {#if !checkingProvider && !providerReady && !setupMode}
        <div class="banner warn">
          No model is connected yet. Say what you want anyway and I will set one up first.
        </div>
      {/if}

      {#if setupMode}
        <div class="setup">
          <h2>First, a model to think with</h2>
          <p class="setup-sub">
            {#if pendingMessage}Your request is saved. This takes a minute, then it carries on.{:else}Pick one and everything else follows.{/if}
          </p>

          <div class="setup-tabs">
            <button class:on={setupMode === 'local'} on:click={openLocalSetup}>On this machine</button>
            <button class:on={setupMode === 'cloud'} on:click={() => { setupMode = 'cloud'; setupError = '' }}>Use a cloud account</button>
          </div>

          {#if setupMode === 'local'}
            {#if pullJob && !pullJob.done}
              <div class="pull">
                <div class="pull-row"><code>{pullJob.model}</code><span>{pullJob.status}</span></div>
                <div class="pull-bar">
                  <div class="pull-fill" style={`width:${pullJob.total ? Math.round(pullJob.completed / pullJob.total * 100) : 4}%`}></div>
                </div>
                <span class="setup-note">Downloading. You can leave this page; it keeps going.</span>
              </div>
            {:else if localModels.length}
              <p class="setup-note">{hostRamGB ? `${hostRamGB} GB of memory detected.` : ''} Anything too big for this machine is greyed out.</p>
              {#each localModels as m}
                <div class="setup-row" class:unfit={!m.fits}>
                  <div>
                    <code>{m.name}</code>{#if m.default}<span class="tag">recommended</span>{/if}
                    <div class="setup-sum">{m.summary}</div>
                  </div>
                  <div class="setup-side">
                    <span class="setup-size">{Math.round(m.size_gb * 10) / 10} GB</span>
                    {#if m.fits}
                      <button class="btn-secondary btn-sm" disabled={setupBusy} on:click={() => installModel(m.name)}>Install</button>
                    {:else}
                      <span class="setup-no">needs {m.min_ram_gb} GB</span>
                    {/if}
                  </div>
                </div>
              {/each}
            {:else}
              <p class="setup-note">No local model runtime is reachable on this machine. Use a cloud account instead.</p>
            {/if}
          {:else if setupMode === 'cloud'}
            {#if cloudModels.length}
              <label class="setup-label" for="cloud-model">Choose a model</label>
              <select id="cloud-model" bind:value={cloudModel}>
                {#each cloudModels as m}<option value={m}>{m}</option>{/each}
              </select>
              <button class="btn-primary btn-sm setup-go" disabled={!cloudModel || setupBusy} on:click={useCloudModel}>
                {setupBusy ? 'Saving…' : 'Use this model'}
              </button>
            {:else}
              <div class="setup-tabs sub">
                {#each CLOUD_PROVIDERS as p}
                  <button class:on={cloudProvider === p.id} on:click={() => { cloudProvider = p.id; setupError = '' }}>{p.label}</button>
                {/each}
              </div>
              <p class="setup-note">Create a key at {CLOUD_PROVIDERS.find(p => p.id === cloudProvider)?.where}, then paste it here. It is stored encrypted on your gateway and never shown again.</p>
              <div class="setup-key">
                <input type="password" placeholder="Paste the API key" bind:value={cloudKey} on:keydown={(e) => { if (e.key === 'Enter') saveCloudKey() }} />
                <button class="btn-secondary btn-sm" disabled={!cloudKey.trim() || setupBusy} on:click={saveCloudKey}>
                  {setupBusy ? 'Checking…' : 'Save'}
                </button>
              </div>
            {/if}
          {/if}

          {#if setupError}<div class="banner err">{setupError}</div>{/if}
          <button class="linkish setup-skip" on:click={() => { setupMode = ''; pendingMessage = '' }}>Not now</button>
        </div>
      {/if}

      <textarea
        bind:value={draft}
        rows="3"
        placeholder="e.g. Every weekday at 7am, check my calendar and tell me what needs preparing."
        on:keydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send() }}
      ></textarea>
      <div class="row">
        <button class="btn-primary" disabled={!draft.trim() || busy} on:click={() => send()}>
          {busy ? 'Thinking…' : 'Continue'}
        </button>
        <span class="hint">or start from one of these</span>
      </div>

      <div class="starters">
        {#each STARTERS as s}
          <button class="starter" on:click={() => send(s)}>{s}</button>
        {/each}
      </div>

      <div class="escape">
        Want full control over tools, branching and code?
        <button class="linkish" on:click={() => go('studio')}>Open Studio</button>
      </div>
    </div>
  {:else}
    <div class="convo">
      {#each turns as t}
        <div class="turn {t.role}">
          <span class="who">{t.role === 'you' ? 'You' : 'Soulacy'}</span>
          <p>{t.text}</p>
        </div>
      {/each}

      {#if phase === 'converse'}
        <div class="reply-row">
          <input
            bind:value={draft}
            placeholder="Answer, or add more detail…"
            on:keydown={(e) => { if (e.key === 'Enter') send() }}
          />
          <button class="btn-primary" disabled={!draft.trim() || busy} on:click={() => send()}>
            {busy ? '…' : 'Send'}
          </button>
        </div>
      {/if}

      {#if phase === 'ready'}
        <div class="plan">
          <h2>Here's what it will do</h2>
          <ul>
            {#each plan as line}<li>{line}</li>{/each}
          </ul>
          {#if understanding?.missing?.length}
            <div class="missing">
              Not covered: {understanding.missing.join(', ')}
            </div>
          {/if}
          <div class="row">
            <button class="btn-primary" disabled={busy} on:click={createAndRun}>Create it and run it now</button>
            <button class="linkish" on:click={() => { phase = 'converse' }}>Change something</button>
          </div>
          <p class="note">It runs once, now, so you can see the result before deciding whether it should run on its own.</p>
        </div>
      {/if}

      {#if phase === 'running'}
        <div class="running">
          <div class="spinner"></div>
          <span>Running it for the first time…</span>
        </div>
      {/if}

      {#if phase === 'result'}
        <div class="result">
          <h2>Here's what it produced</h2>
          <pre>{output}</pre>

          {#if deliveryWarning}
            <div class="banner warn">{deliveryWarning}</div>
          {/if}

          {#if scheduled}
            <div class="banner ok">It will run on its own from now on.</div>
          {:else if schedulePending}
            <div class="schedule-offer">
              <span>Want this to happen automatically from now on?</span>
              <button class="btn-primary btn-sm" disabled={scheduling} on:click={activateSchedule}>
                {scheduling ? 'Starting…' : 'Yes, run it on schedule'}
              </button>
            </div>
          {/if}

          <div class="row">
            <button class="linkish" on:click={() => go('agents')}>See it under Deployed</button>
            <button class="linkish" on:click={restart}>Build another</button>
          </div>
        </div>
      {/if}

      {#if error}<div class="banner err">{error}</div>{/if}
    </div>
  {/if}
</div>

<style>
  .page { max-width: 760px; margin: 0 auto; padding: 1.5rem 1rem 3rem; }
  .page-head { display: flex; justify-content: flex-end; margin-bottom: .4rem; }

  .hero h1 { font-size: 1.8rem; font-weight: 600; margin-bottom: .4rem; }
  .sub { color: #6b7294; font-size: .9rem; margin-bottom: 1.2rem; }

  textarea, input {
    width: 100%; background: #11131f; border: 1px solid #1a1e36; border-radius: 10px;
    padding: .8rem .9rem; color: inherit; font: inherit; font-size: .92rem; resize: vertical;
  }
  textarea:focus, input:focus { outline: none; border-color: #8b85ff; }

  .row { display: flex; align-items: center; gap: .8rem; margin-top: .8rem; flex-wrap: wrap; }
  .hint { color: #6b7294; font-size: .78rem; }

  .starters { display: grid; gap: .5rem; margin-top: 1rem; }
  .starter {
    text-align: left; background: #11131f; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .65rem .8rem; color: #a9b0cc; font-size: .84rem; cursor: pointer;
  }
  .starter:hover { border-color: #8b85ff; color: #e6e9f5; }

  .escape { margin-top: 2rem; color: #6b7294; font-size: .78rem; }

  /* Setup, inline. The point is that the person never leaves this screen and
     never loses the thing they were trying to do. */
  .setup { border: 1px solid rgba(232,168,72,.35); background: rgba(232,168,72,.06);
           border-radius: 10px; padding: 1rem 1.1rem; margin-bottom: 1rem; }
  .setup h2 { font-size: 1rem; font-weight: 600; margin-bottom: .2rem; }
  .setup-sub { color: #a9b0cc; font-size: .82rem; margin-bottom: .8rem; }
  .setup-tabs { display: flex; gap: .4rem; margin-bottom: .7rem; flex-wrap: wrap; }
  .setup-tabs button { background: #11131f; border: 1px solid #1a1e36; color: #a9b0cc;
                       border-radius: 7px; padding: .35rem .7rem; font-size: .78rem; cursor: pointer; }
  .setup-tabs button.on { border-color: #8b85ff; color: #e6e9f5; }
  .setup-tabs.sub button { font-size: .74rem; }
  .setup-note { color: #6b7294; font-size: .75rem; margin-bottom: .5rem; }
  .setup-label { display: block; color: #6b7294; font-size: .74rem; margin-bottom: .25rem; }
  .setup select { width: 100%; background: #11131f; border: 1px solid #1a1e36; border-radius: 7px;
                  padding: .4rem .5rem; color: inherit; font-size: .8rem; }
  .setup-go { margin-top: .6rem; }
  .setup-key { display: flex; gap: .4rem; }
  .setup-key input { flex: 1; background: #11131f; border: 1px solid #1a1e36; border-radius: 7px;
                     padding: .4rem .55rem; color: inherit; font-size: .8rem; }
  .setup-row { display: flex; align-items: center; justify-content: space-between; gap: 1rem;
               padding: .45rem .6rem; border: 1px solid #1a1e36; border-radius: 8px;
               background: #11131f; margin-bottom: .3rem; }
  .setup-row.unfit { opacity: .55; }
  .setup-row code { color: #8b85ff; font-size: .8rem; }
  .setup-sum { color: #6b7294; font-size: .72rem; margin-top: .1rem; }
  .setup-side { display: flex; align-items: center; gap: .5rem; flex-shrink: 0; }
  .setup-size { color: #a9b0cc; font-size: .72rem; }
  .setup-no { color: #6b7294; font-size: .7rem; }
  .tag { background: rgba(76,175,130,.16); color: #4caf82; font-size: .62rem;
         padding: .05rem .35rem; border-radius: 4px; margin-left: .35rem; }
  .setup-skip { margin-top: .6rem; }
  .pull-row { display: flex; justify-content: space-between; font-size: .78rem; }
  .pull-row code { color: #8b85ff; }
  .pull-bar { height: 5px; background: #1a1e36; border-radius: 3px; overflow: hidden; margin: .4rem 0 .3rem; }
  .pull-fill { height: 100%; background: #8b85ff; transition: width .3s ease; }

  .convo { display: flex; flex-direction: column; gap: .9rem; }
  .turn .who { color: #6b7294; font-size: .68rem; text-transform: uppercase; letter-spacing: .06em; }
  .turn p { margin-top: .15rem; font-size: .92rem; line-height: 1.5; }
  .turn.you p { color: #e6e9f5; }
  .turn.soulacy p { color: #a9b0cc; }

  .reply-row { display: flex; gap: .6rem; margin-top: .4rem; }
  .reply-row input { flex: 1; }

  .plan {
    border: 1px solid rgba(139,133,255,.3); background: rgba(139,133,255,.06);
    border-radius: 10px; padding: 1rem 1.1rem; margin-top: .6rem;
  }
  .plan h2, .result h2 { font-size: 1rem; font-weight: 600; margin-bottom: .5rem; }
  .plan ul { margin: 0 0 .6rem 1rem; }
  .plan li { font-size: .88rem; line-height: 1.6; color: #e6e9f5; }
  .missing { color: #e8a848; font-size: .8rem; margin-bottom: .5rem; }
  .note { color: #6b7294; font-size: .76rem; margin-top: .6rem; }

  .running { display: flex; align-items: center; gap: .7rem; color: #a9b0cc; font-size: .88rem; padding: 1rem 0; }
  .spinner {
    width: 16px; height: 16px; border: 2px solid #1a1e36; border-top-color: #8b85ff;
    border-radius: 50%; animation: spin .8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }

  .result pre {
    background: #11131f; border: 1px solid #1a1e36; border-radius: 10px;
    padding: .9rem 1rem; white-space: pre-wrap; word-break: break-word;
    font-size: .86rem; line-height: 1.55; color: #e6e9f5; max-height: 420px; overflow: auto;
  }

  .schedule-offer {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap;
    margin-top: .9rem; padding: .7rem .9rem; border: 1px solid #1a1e36; border-radius: 10px; background: #11131f;
  }
  .schedule-offer span { font-size: .88rem; }

  .banner { padding: .7rem .9rem; border-radius: 8px; font-size: .84rem; margin-top: .8rem; }
  .warn { background: rgba(232,168,72,.1); border: 1px solid rgba(232,168,72,.32); color: #e8a848; }
  .ok   { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
</style>
