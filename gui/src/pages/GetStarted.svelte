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

  let unreachable = false
  onMount(async () => {
    // A model has to exist before any of this can work. Saying so here beats
    // failing three questions in.
    try {
      const doctor = await api.providers.doctor()
      const providers = doctor.providers || []
      unreachable = providers.length > 0 && !providers.some(p => p.status === 'ok')
    } catch {
      unreachable = false
    }
  })

  async function send(text) {
    const message = (text ?? draft).trim()
    if (!message || busy) return
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
  {#if phase === 'ask'}
    <div class="hero">
      <h1>What would you like help with?</h1>
      <p class="sub">Describe it in your own words. Soulacy works out the rest and shows you before anything runs.</p>

      {#if unreachable}
        <div class="banner warn">
          No model is available yet, so nothing can run. <button class="linkish" on:click={() => go('providers')}>Set one up first</button>.
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
