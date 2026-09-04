<!--
  PageTour.svelte — "Show me around", for the screen you are actually on.

  The 24-stop tour answers "what is all this". This answers the question people
  ask far more often: "I am on this screen — why, and what do I do here." The
  story is the same one either way (how an agent comes to do a real job on its
  own) told from wherever you are standing, and the server writes it against
  the live install so it never explains a step you finished last week.
-->
<script>
  import { createEventDispatcher } from 'svelte'
  import { api } from './api.js'

  export let page = ''
  export let open = false

  const dispatch = createEventDispatcher()

  let story = null
  let error = ''
  let loading = false
  let loadedFor = ''

  // Re-fetch when the panel opens on a different screen. The narrative depends
  // on live state, so a cached one goes stale the moment the user changes
  // anything — cheap call, always fresh.
  $: if (open && page && page !== loadedFor) load(page)
  $: if (!open) loadedFor = ''

  async function load(p) {
    loadedFor = p
    loading = true
    error = ''
    story = null
    try {
      story = await api.tour(p)
    } catch (e) {
      error = e?.status === 404
        ? 'There is no tour for this screen yet.'
        : (e?.message || 'Could not load the tour.')
    } finally {
      loading = false
    }
  }
</script>

{#if open}
  <div class="pt-scrim" role="presentation" on:click={() => dispatch('close')}></div>
  <aside class="pt-panel" role="dialog" aria-modal="true" aria-labelledby="pt-title">
    <header class="pt-head">
      <div>
        <span class="pt-eyebrow">Show me around</span>
        <h2 id="pt-title">{story?.chapter ? capitalise(story.chapter) : 'This screen'}</h2>
      </div>
      <button class="pt-close" on:click={() => dispatch('close')} aria-label="Close">✕</button>
    </header>

    {#if loading}
      <p class="pt-muted">Reading your setup…</p>
    {:else if error}
      <p class="pt-error">{error}</p>
    {:else if story}
      <p class="pt-outcome">
        <span>It all leads to one thing:</span>
        <strong>{story.outcome}</strong>
      </p>
      <p class="pt-position">{story.position}</p>

      {#each story.beats as beat}
        <section class="pt-beat">
          {#if beat.heading}<h3>{beat.heading}</h3>{/if}
          <p>{beat.text}</p>
        </section>
      {/each}

      <div class="pt-actions">
        {#if story.nextAction}
          <button class="pt-btn primary" on:click={() => dispatch('act', story)}>{story.nextLabel}</button>
        {/if}
        <button class="pt-btn" on:click={() => dispatch('fulltour')}>Take the full tour</button>
      </div>
    {/if}
  </aside>
{/if}

<script context="module">
  function capitalise(s) { return s ? s[0].toUpperCase() + s.slice(1) : s }
</script>

<style>
  .pt-scrim { position: fixed; inset: 0; background: rgba(6, 8, 18, 0.55); z-index: 940; border: none; }
  .pt-panel {
    position: fixed; top: 0; right: 0; bottom: 0; z-index: 941;
    width: min(430px, 92vw); overflow-y: auto;
    background: #14172a; border-left: 1px solid #2a2f4a;
    padding: 1.15rem 1.3rem 2rem;
    box-shadow: -18px 0 50px rgba(0,0,0,0.5);
  }
  .pt-head { display: flex; align-items: flex-start; gap: .5rem; margin-bottom: .9rem; }
  .pt-eyebrow { font-size: .66rem; letter-spacing: .09em; text-transform: uppercase; color: #6f76a0; }
  .pt-head h2 { font-size: 1.15rem; font-weight: 650; color: #f2f3fb; margin-top: .1rem; }
  .pt-close { margin-left: auto; background: none; color: #6f76a0; font-size: .9rem; padding: .2rem .35rem; border-radius: 6px; }
  .pt-close:hover { background: #1e2238; color: #d4d7ea; }

  .pt-outcome {
    font-size: .82rem; line-height: 1.5; color: #b6bbdb;
    background: rgba(108, 99, 255, 0.1); border: 1px solid #2f3459;
    border-radius: 10px; padding: .6rem .75rem;
  }
  .pt-outcome span { display: block; color: #7d84ae; font-size: .72rem; margin-bottom: .15rem; }
  .pt-outcome strong { color: #dfe2ff; font-weight: 600; }
  .pt-position { font-size: .75rem; color: #7d84ae; margin: .55rem 0 .2rem; font-style: italic; }

  .pt-beat { margin-top: .95rem; }
  .pt-beat h3 { font-size: .7rem; letter-spacing: .07em; text-transform: uppercase; color: #6f76a0; margin-bottom: .3rem; }
  .pt-beat p { font-size: .87rem; line-height: 1.62; color: #d3d6ec; }

  .pt-actions { display: flex; flex-wrap: wrap; gap: .5rem; margin-top: 1.4rem; }
  .pt-btn {
    background: #1c2038; color: #dfe2ff; border: 1px solid #2f3459;
    font-size: .8rem; font-weight: 600; padding: .45rem .9rem; border-radius: 8px;
  }
  .pt-btn:hover { border-color: #6c63ff; }
  .pt-btn.primary { background: #6c63ff; border-color: #6c63ff; color: #fff; }
  .pt-btn.primary:hover { filter: brightness(1.08); }

  .pt-muted { color: #7d84ae; font-size: .85rem; }
  .pt-error { color: #ff9d9d; font-size: .85rem; }
</style>
