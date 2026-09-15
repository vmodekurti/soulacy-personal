<script>
  // About You — what Soulacy understands about the person, and which senses
  // may add to it.
  //
  // Two rules shape this screen. Every line says where it came from, because
  // a model you cannot audit is one you cannot trust. And nothing is watched
  // until you switch it on, with the reason stated next to the switch.
  import { onMount } from 'svelte'
  import TourButton from '../lib/TourButton.svelte'
  import { api } from '../lib/api.js'
  import { filledSections, fillProgress, nextStep, provenance, isGuess, SECTION_ORDER, SECTION_TITLES } from '../lib/personmodel.js'

  let sections = {}
  let senses = []
  let owner = ''
  let summary = ''
  let loading = true
  let error = ''
  let notice = ''
  let unavailable = false

  // Adding or correcting a line by hand.
  let draft = { section: 'identity', key: '', summary: '' }
  let saving = false

  $: filled = filledSections(sections)
  $: progress = fillProgress(sections)
  $: todo = nextStep(sections, senses)

  async function load() {
    loading = true
    error = ''
    try {
      const model = await api.person.model()
      sections = model?.sections || {}
      owner = model?.owner || ''
      summary = model?.summary || ''
      unavailable = false
    } catch (e) {
      if (/not enabled/i.test(e.message || '')) {
        unavailable = true
      } else {
        error = e.message || 'Could not read your model'
      }
      sections = {}
    }
    try {
      const res = await api.person.senses()
      senses = res?.senses || []
    } catch (_) {
      senses = []
    }
    loading = false
  }

  async function toggleSense(sense, enabled) {
    error = ''
    notice = ''
    try {
      const res = await api.person.setSense(sense, enabled)
      if (!enabled && res?.forgotten) {
        notice = `Switched off. ${res.forgotten} ${res.forgotten === 1 ? 'line' : 'lines'} it had worked out ${res.forgotten === 1 ? 'was' : 'were'} deleted, along with the signals behind ${res.forgotten === 1 ? 'it' : 'them'}.`
      } else if (!enabled) {
        notice = 'Switched off. It had not worked anything out yet.'
      }
      await load()
    } catch (e) {
      error = e.message || 'Could not change that switch'
    }
  }

  async function saveDraft() {
    const key = draft.key.trim()
    const text = draft.summary.trim()
    if (!key || !text) return
    saving = true
    error = ''
    notice = ''
    try {
      // A line typed here is yours, so it outranks every sense and agent
      // from now on.
      await api.person.put({ section: draft.section, key, summary: text })
      draft = { section: draft.section, key: '', summary: '' }
      await load()
    } catch (e) {
      error = e.message || 'Could not save that'
    } finally {
      saving = false
    }
  }

  async function remove(section, key) {
    error = ''
    notice = ''
    try {
      await api.person.remove(section, key)
      await load()
    } catch (e) {
      error = e.message || 'Could not delete that'
    }
  }

  async function forgetAll() {
    if (!confirm('Forget everything Soulacy understands about you? Agents will start from nothing.')) return
    error = ''
    try {
      const res = await api.person.purge()
      notice = `Forgotten. ${res?.removed || 0} lines removed.`
      await load()
    } catch (e) {
      error = e.message || 'Could not forget it'
    }
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1>About You</h1>
      <p>What Soulacy understands about you. Every line says where it came from; correct or delete any of it.</p>
    </div>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↻ Refresh</button>
      <TourButton />
    </div>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}

  {#if unavailable}
    <section class="panel empty-state">
      <h2>Not switched on</h2>
      <p>This gateway is not keeping a person model. It arrives with the next update.</p>
    </section>
  {:else}
    <section class="panel progress-panel">
      <div class="progress-head">
        <div>
          <h2>{progress.entries === 0 ? 'Nothing known yet' : `${progress.entries} ${progress.entries === 1 ? 'line' : 'lines'} across ${progress.filled} of ${progress.total} areas`}</h2>
          {#if todo}<p>{todo}</p>{/if}
        </div>
        {#if owner}<small class="owner">Yours alone · {owner}</small>{/if}
      </div>
      <div class="bar" role="img" aria-label={`${progress.percent}% of areas have something in them`}>
        <span style={`width:${progress.percent}%`}></span>
      </div>
    </section>

    {#if loading && !filled.length}
      <div class="empty">Reading your model…</div>
    {:else if !filled.length}
      <section class="panel empty-state">
        <h2>Soulacy knows nothing about you yet</h2>
        <p>
          That is the honest starting point. The quickest way to change it is to talk to the
          <strong>Getting to Know You</strong> agent in Chat: a few questions, and every other agent
          has something to work with. You can also add a line yourself below, or switch on a sense.
        </p>
      </section>
    {:else}
      {#each filled as section}
        <section class="panel">
          <div class="panel-title">
            <h2>{section.title}</h2>
            <span>{section.blurb}</span>
          </div>
          <ul class="entries">
            {#each section.entries as entry}
              {@const from = provenance(entry)}
              <li>
                <div class="line">
                  <span class="summary" class:guess={isGuess(entry)}>{entry.summary}</span>
                  <button class="link danger" on:click={() => remove(section.id, entry.key)} title="Delete this line">Delete</button>
                </div>
                <div class="meta">
                  <span class={`tag ${from.tone}`}>{from.label}</span>
                  {#if isGuess(entry)}<span class="tag guess-tag">a guess</span>{/if}
                  {#if from.detail}<span class="said">{from.detail}</span>{/if}
                  {#if entry.expires_at}<span class="expiry">expires</span>{/if}
                </div>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    {/if}

    <section class="panel">
      <div class="panel-title">
        <h2>Add or correct a line</h2>
        <span>Anything you write here outranks every sense and agent from then on.</span>
      </div>
      <div class="draft">
        <select bind:value={draft.section} aria-label="Which area">
          {#each SECTION_ORDER as id}<option value={id}>{SECTION_TITLES[id]}</option>{/each}
        </select>
        <input bind:value={draft.key} placeholder="Name for this line, e.g. home" aria-label="Key" />
        <input bind:value={draft.summary} placeholder="In your own words, e.g. Home is in Oak Park"
               aria-label="What is true" on:keydown={(e) => e.key === 'Enter' && saveDraft()} />
        <button on:click={saveDraft} disabled={saving || !draft.key.trim() || !draft.summary.trim()}>Save</button>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title">
        <h2>What may Soulacy notice?</h2>
        <span>Every sense is off until you switch it on. Switching one off deletes what it worked out.</span>
      </div>
      {#if !senses.length}
        <div class="empty">This gateway collects no signals.</div>
      {:else}
        <ul class="senses">
          {#each senses as sense}
            <li>
              <label>
                <input type="checkbox" checked={sense.enabled}
                       on:change={(e) => toggleSense(sense.sense, e.currentTarget.checked)} />
                <span class="sense-name">{sense.sense}</span>
              </label>
              <p>{sense.purpose}</p>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    {#if progress.entries > 0}
      <section class="panel danger-panel">
        <div class="panel-title">
          <h2>Forget everything</h2>
          <span>Deletes every line above. Agents start from nothing.</span>
        </div>
        <button class="danger" on:click={forgetAll}>Forget everything about me</button>
      </section>
    {/if}
  {/if}
</div>

<style>
  .page { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; overflow: auto; }
  .page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
  .header-actions { display: flex; align-items: center; gap: .5rem; }
  h1, h2, p { margin: 0; }
  h1 { font-size: 1.2rem; }
  h2 { font-size: .95rem; }
  p { color: #858caf; font-size: .82rem; line-height: 1.45; }
  .banner { border-radius: 8px; padding: .7rem .85rem; font-size: .82rem; }
  .err { color: #ff9a9a; background: rgba(255, 90, 90, .12); border: 1px solid rgba(255, 90, 90, .25); }
  .ok { color: #9ae6b4; background: rgba(72, 187, 120, .12); border: 1px solid rgba(72, 187, 120, .25); }
  .panel { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; }
  .panel-title { display: flex; align-items: baseline; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .panel-title span { color: #777fa5; font-size: .75rem; }
  .progress-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .owner { color: #777fa5; font-size: .72rem; white-space: nowrap; }
  .bar { margin-top: .7rem; height: 6px; border-radius: 99px; background: #1a1e33; overflow: hidden; }
  .bar span { display: block; height: 100%; background: linear-gradient(90deg, #6c63ff, #22d3ee); transition: width .3s ease; }
  .entries { list-style: none; margin: .75rem 0 0; padding: 0; display: flex; flex-direction: column; gap: .7rem; }
  .entries li { border-top: 1px solid #1b1f36; padding-top: .7rem; }
  .entries li:first-child { border-top: 0; padding-top: 0; }
  .line { display: flex; align-items: baseline; justify-content: space-between; gap: .75rem; }
  .summary { color: #e8eaf6; font-size: .88rem; }
  .summary.guess { color: #b9bedd; font-style: italic; }
  .meta { display: flex; align-items: center; gap: .45rem; margin-top: .3rem; flex-wrap: wrap; }
  .tag { font-size: .68rem; padding: .12rem .45rem; border-radius: 99px; border: 1px solid transparent; }
  .tag.you { color: #9ae6b4; background: rgba(72, 187, 120, .12); border-color: rgba(72, 187, 120, .25); }
  .tag.agent { color: #c4b5fd; background: rgba(139, 92, 246, .12); border-color: rgba(139, 92, 246, .25); }
  .tag.sense { color: #7dd3fc; background: rgba(56, 189, 248, .12); border-color: rgba(56, 189, 248, .25); }
  .guess-tag { color: #fbbf77; background: rgba(251, 146, 60, .12); border-color: rgba(251, 146, 60, .25); }
  .said { color: #8f96bb; font-size: .74rem; font-style: italic; }
  .expiry { color: #777fa5; font-size: .7rem; }
  .link { background: none; border: 0; padding: 0; font-size: .74rem; cursor: pointer; color: #8f96bb; }
  .link.danger { color: #ff9a9a; }
  .draft { display: grid; grid-template-columns: 170px 200px minmax(0, 1fr) auto; gap: .5rem; margin-top: .75rem; }
  .senses { list-style: none; margin: .75rem 0 0; padding: 0; display: flex; flex-direction: column; gap: .8rem; }
  .senses label { display: flex; align-items: center; gap: .5rem; color: #e8eaf6; font-size: .88rem; }
  .sense-name { text-transform: capitalize; }
  .senses p { margin-top: .25rem; margin-left: 1.55rem; }
  .empty, .empty-state p { color: #8f96bb; }
  .empty { padding: .8rem; }
  .empty-state { display: flex; flex-direction: column; gap: .4rem; }
  .danger { background: rgba(184, 65, 65, .22); border: 1px solid rgba(255, 90, 90, .3); color: #ff9a9a; margin-top: .75rem; }
  .danger-panel { border-color: rgba(255, 90, 90, .22); }
  @media (max-width: 860px) {
    .draft { grid-template-columns: 1fr; }
  }
</style>
