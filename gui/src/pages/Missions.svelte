<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import TourButton from '../lib/TourButton.svelte'

  let missions = []
  let loading = true
  let busy = false
  let error = ''
  let notice = ''
  let composing = false
  let planning = false
  let plan = null
  let objective = ''
  let schedule = 'weekday'
  let customCron = ''
  let runAt = ''
  let channel = ''
  let destination = ''
  let timer

  const presets = {
    daily: '0 8 * * *',
    weekday: '0 8 * * 1-5',
    weekly: '0 9 * * 1',
  }

  $: activeCount = missions.filter((m) => m.status === 'active' || m.status === 'blocked').length
  $: attentionCount = missions.filter((m) => m.status === 'blocked' || m.runner?.blocked).length

  async function load(quiet = false) {
    if (!quiet) loading = true
    try {
      const response = await api.missions.list()
      missions = Array.isArray(response?.missions) ? response.missions : []
      if (!quiet) error = ''
    } catch (e) {
      if (!quiet) error = e?.message || 'Could not load missions.'
    } finally {
      loading = false
    }
  }

  function openComposer() {
    objective = ''
    plan = null
    schedule = 'weekday'
    customCron = ''
    runAt = ''
    channel = ''
    destination = ''
    error = ''
    composing = true
  }

  async function buildPlan() {
    if (objective.trim().length < 8) return
    planning = true
    error = ''
    try {
      const response = await api.missions.plan(objective.trim())
      plan = response.plan
    } catch (e) {
      error = e?.message || 'Genie could not plan this mission.'
    } finally {
      planning = false
    }
  }

  function missionSchedule() {
    if (schedule === 'once') {
      if (!runAt) throw new Error('Choose when this mission should run.')
      return { at: new Date(runAt).toISOString(), cron: '' }
    }
    const cron = schedule === 'custom' ? customCron.trim() : presets[schedule]
    if (!cron) throw new Error('Enter a cron schedule.')
    return { cron, at: '' }
  }

  async function activate() {
    busy = true
    error = ''
    try {
      if ((channel.trim() && !destination.trim()) || (!channel.trim() && destination.trim())) {
        throw new Error('Delivery channel and destination must be entered together.')
      }
      const timing = missionSchedule()
      const response = await api.missions.create({
        title: plan.title,
        objective: plan.objective,
        finish_line: plan.finish_line,
        ...timing,
        channel: channel.trim(),
        to: destination.trim(),
      })
      notice = response.message || 'Mission activated.'
      composing = false
      await load(true)
    } catch (e) {
      error = e?.message || 'Could not activate this mission.'
    } finally {
      busy = false
    }
  }

  async function act(mission, action) {
    const labels = { pause: 'pause', resume: 'resume', complete: 'mark complete', cancel: 'cancel' }
    if ((action === 'complete' || action === 'cancel') && !confirm(`Do you want to ${labels[action]} ${mission.title}?`)) return
    busy = true
    error = ''
    try {
      let response
      if (action === 'pause') response = await api.missions.pause(mission.id)
      if (action === 'resume') response = await api.missions.resume(mission.id)
      if (action === 'complete') response = await api.missions.complete(mission.id, mission.progress || 'Mission completed')
      if (action === 'cancel') response = await api.missions.cancel(mission.id)
      notice = response?.message || `Mission ${labels[action]}d.`
      await load(true)
    } catch (e) {
      error = e?.message || `Could not ${labels[action]} the mission.`
    } finally {
      busy = false
    }
  }

  function scheduleLabel(mission) {
    if (mission.at) return `One time: ${new Date(mission.at).toLocaleString()}`
    return mission.cron ? `Cron: ${mission.cron}` : 'No schedule'
  }

  onMount(() => {
    load()
    timer = setInterval(() => { if (!document.hidden) load(true) }, 15000)
  })
  onDestroy(() => clearInterval(timer))
</script>

<svelte:head><title>Missions | Soulacy</title></svelte:head>

<main class="page">
  <header>
    <div><p class="eyebrow">ALWAYS ON GENIE</p><h1>Missions</h1><p class="lede">Give Genie a standing responsibility. Keep the finish line, progress, permissions, and stop controls visible.</p></div>
    <div class="actions"><button class="secondary" on:click={() => load()} disabled={loading}>Refresh</button><TourButton /><button class="primary" on:click={openComposer}>+ New mission</button></div>
  </header>

  {#if error}<div class="alert error" role="alert">{error}</div>{/if}
  {#if notice}<div class="alert success" role="status">{notice}</div>{/if}

  <section class="overview">
    <article><strong>{activeCount}</strong><span>In Genie's care</span></article>
    <article><strong>{attentionCount}</strong><span>Need your attention</span></article>
    <div><h2>Quiet by default</h2><p>Genie checks the mission on schedule and speaks up for meaningful progress, a blocker, completion, failure, or required action.</p></div>
  </section>

  {#if loading}
    <div class="empty">Loading missions...</div>
  {:else if missions.length === 0}
    <section class="empty"><div class="mark">◉</div><h2>Nothing in Genie's care yet</h2><p>Start with an ongoing responsibility such as a morning briefing, a price watch, or preparation for an event.</p><button class="primary" on:click={openComposer}>Create your first mission</button></section>
  {:else}
    <section class="grid">
      {#each missions as mission (mission.id)}
        <article class="card" class:needs-attention={mission.status === 'blocked' || mission.runner?.blocked}>
          <div class="card-head"><div><span class="status {mission.status}">{mission.status}</span><h2>{mission.title}</h2></div><span class:live={mission.runner?.running} class="runner">{mission.runner?.running ? 'Running now' : mission.runner ? 'Runner ready' : 'Runner stopped'}</span></div>
          <p class="objective">{mission.objective}</p>
          <div class="finish"><span>Finish line</span><strong>{mission.finish_line}</strong></div>
          <dl>
            <div><dt>Progress</dt><dd>{mission.progress || 'No update yet'}</dd></div>
            {#if mission.blocker}<div class="blocker"><dt>Blocker</dt><dd>{mission.blocker}</dd></div>{/if}
            <div><dt>Next action</dt><dd>{mission.next_action || 'Waiting for review'}</dd></div>
          </dl>
          <div class="meta"><span>{scheduleLabel(mission)}</span>{#if mission.runner?.next}<span>Next run: {new Date(mission.runner.next).toLocaleString()}</span>{/if}{#if mission.channel}<span>Delivery: {mission.channel}</span>{/if}</div>
          <div class="card-actions">
            {#if mission.status === 'active' || mission.status === 'blocked'}<button class="secondary" disabled={busy} on:click={() => act(mission, 'pause')}>Pause</button>{/if}
            {#if mission.status === 'paused'}<button class="primary" disabled={busy} on:click={() => act(mission, 'resume')}>Resume</button>{/if}
            {#if !['completed','cancelled'].includes(mission.status)}<button class="secondary" disabled={busy} on:click={() => act(mission, 'complete')}>Complete</button><button class="danger" disabled={busy} on:click={() => act(mission, 'cancel')}>Cancel</button>{/if}
            {#if mission.monitor_id}<button class="link" on:click={() => location.hash = 'schedule'}>View runs</button>{/if}
          </div>
        </article>
      {/each}
    </section>
  {/if}
</main>

{#if composing}
  <div class="backdrop" role="presentation" on:click|self={() => composing = false}>
    <div class="composer" role="dialog" aria-modal="true" aria-labelledby="mission-title" tabindex="-1">
      <div class="composer-head"><div><p class="eyebrow">NEW MISSION</p><h2 id="mission-title">What should Genie keep handling?</h2></div><button class="close" aria-label="Close" on:click={() => composing = false}>×</button></div>
      {#if !plan}
        <p>Describe the ongoing outcome. Genie will propose a visible contract before anything starts.</p>
        <textarea bind:value={objective} rows="6" maxlength="2000" placeholder="Every weekday morning, prepare a concise leadership briefing from my selected publications and tell me when a source is blocked."></textarea>
        <div class="form-actions"><button class="secondary" on:click={() => composing = false}>Cancel</button><button class="primary" disabled={planning || objective.trim().length < 8} on:click={buildPlan}>{planning ? 'Planning...' : 'Plan mission'}</button></div>
      {:else}
        <label><span>Mission name</span><input bind:value={plan.title} maxlength="120" /></label>
        <label><span>Objective</span><textarea bind:value={plan.objective} rows="4" maxlength="2000"></textarea></label>
        <label><span>Finish line</span><textarea bind:value={plan.finish_line} rows="3" maxlength="1000"></textarea></label>
        <div class="two">
          <label><span>Schedule</span><select bind:value={schedule}><option value="weekday">Every weekday at 8 AM</option><option value="daily">Every day at 8 AM</option><option value="weekly">Every Monday at 9 AM</option><option value="once">One time</option><option value="custom">Custom cron</option></select></label>
          {#if schedule === 'once'}<label><span>Run at</span><input type="datetime-local" bind:value={runAt} /></label>{/if}
          {#if schedule === 'custom'}<label><span>Cron</span><input bind:value={customCron} placeholder="0 8 * * 1-5" /></label>{/if}
        </div>
        <div class="two"><label><span>Delivery channel, optional</span><input bind:value={channel} placeholder="mobile" /></label><label><span>Destination</span><input bind:value={destination} placeholder="all" /></label></div>
        <section class="guardrails"><strong>Standing contract</strong>{#each plan.guardrails || [] as item}<span>✓ {item}</span>{/each}</section>
        <div class="form-actions"><button class="secondary" on:click={() => plan = null}>Back</button><button class="primary" disabled={busy || !plan.title.trim() || !plan.finish_line.trim()} on:click={activate}>{busy ? 'Activating...' : 'Activate mission'}</button></div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .page{display:flex;flex-direction:column;gap:18px;max-width:1180px}.page>header{display:flex;align-items:flex-start;justify-content:space-between;gap:18px}.eyebrow{margin:0 0 5px;color:#67ddd4;font-size:.7rem;font-weight:800;letter-spacing:.11em}.page h1,.composer h2{margin:0}.lede{max-width:720px;margin:6px 0 0;color:#98a3b7;line-height:1.5}.actions,.card-actions,.form-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.primary,.secondary,.danger,.link,.close{border-radius:8px;padding:8px 11px;font:inherit;font-size:.78rem;cursor:pointer}.primary{border:1px solid #54d9cf;background:#54d9cf;color:#071719;font-weight:800}.secondary{border:1px solid #39435a;background:#20283a;color:inherit}.danger{border:1px solid #663845;background:#2c1c27;color:#f29aa8}.link{margin-left:auto;border:0;background:transparent;color:#67d7cf}.overview{display:grid;grid-template-columns:150px 170px 1fr;gap:11px}.overview article,.overview>div{padding:17px;border:1px solid #2b3549;border-radius:12px;background:#141b29}.overview article{display:flex;flex-direction:column}.overview article strong{font-size:1.7rem;color:#69ddd5}.overview article span{color:#919caf;font-size:.72rem}.overview h2{margin:0 0 5px;font-size:.9rem}.overview p{margin:0;color:#929db0;font-size:.75rem;line-height:1.5}.alert,.empty{padding:17px;border-radius:10px}.alert.error{background:#361f29;color:#ffabb4}.alert.success{background:#14352d;color:#79e0ae}.empty{text-align:center;border:1px dashed #374159;color:#929caf;padding:38px}.empty h2{color:#e8ebf2}.mark{font-size:1.7rem;color:#63ddd3}.grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.card{display:flex;flex-direction:column;gap:13px;padding:18px;border:1px solid #2d374b;border-radius:14px;background:#151b29}.card.needs-attention{border-color:#a97945}.card-head{display:flex;justify-content:space-between;gap:12px;align-items:flex-start}.card h2{margin:6px 0 0;font-size:1.04rem}.status{display:inline-flex;padding:2px 7px;border-radius:999px;background:#18382f;color:#7cdfac;font-size:.63rem;text-transform:uppercase}.status.paused{background:#2a2e3d;color:#aeb6c8}.status.blocked{background:#4a331f;color:#f1b96f}.status.completed{background:#1c3348;color:#80c9f0}.status.cancelled{background:#35242b;color:#d2939f}.runner{font-size:.67rem;color:#8792a6}.runner.live{color:#72ddb0}.objective{margin:0;color:#c7cfdd;line-height:1.5}.finish{display:flex;flex-direction:column;gap:4px;padding:11px;border-left:3px solid #53d6cd;background:#101624}.finish span,dt{color:#818ca1;font-size:.65rem;text-transform:uppercase;letter-spacing:.05em}.finish strong{font-size:.78rem;line-height:1.45}dl{display:flex;flex-direction:column;gap:8px;margin:0}dl>div{display:grid;grid-template-columns:86px 1fr;gap:8px}dd{margin:0;color:#b7c0d0;font-size:.74rem;line-height:1.45}.blocker dd{color:#edb471}.meta{display:flex;gap:7px;flex-wrap:wrap}.meta span{padding:4px 7px;border-radius:999px;background:#20283a;color:#8f9ab0;font-size:.65rem}.card-actions{margin-top:auto}.backdrop{position:fixed;inset:0;z-index:100;display:grid;place-items:center;padding:20px;background:#03060ed1}.composer{width:min(720px,100%);max-height:92vh;overflow:auto;padding:22px;border:1px solid #3b465e;border-radius:15px;background:#141a29}.composer-head{display:flex;justify-content:space-between;align-items:flex-start}.close{padding:1px 8px;border:0;background:transparent;color:#aeb7c8;font-size:1.6rem}.composer>p{color:#aeb7c8;line-height:1.5}.composer label{display:grid;gap:6px;margin-top:12px;color:#8f99ad;font-size:.75rem}input,select,textarea{box-sizing:border-box;width:100%;padding:10px 12px;border:1px solid #323c52;border-radius:8px;background:#101624;color:inherit;font:inherit}textarea{resize:vertical;line-height:1.5}.two{display:grid;grid-template-columns:1fr 1fr;gap:10px}.guardrails{display:flex;flex-direction:column;gap:5px;margin-top:16px;padding:12px;background:#101624;border-left:3px solid #50d1c8}.guardrails span{color:#9ba7ba;font-size:.7rem}.form-actions{justify-content:flex-end;margin-top:18px}@media(max-width:800px){.grid{grid-template-columns:1fr}.overview{grid-template-columns:1fr 1fr}.overview>div{grid-column:1/-1}}@media(max-width:600px){.page>header,.overview,.two{display:grid;grid-template-columns:1fr}.overview>div{grid-column:auto}.link{margin-left:0}}
</style>
