<script>
  // The first thing the chat-first shell says. It stands where a hero banner
  // used to: instead of describing the product, it reports on the person's day.
  // On a quiet day that is a greeting and one sentence. Only when something is
  // waiting does one more line appear, and it leaves as soon as it is answered.
  import { createEventDispatcher } from 'svelte'
  export let brief = null
  export let busy = false
  export let error = ''
  const dispatch = createEventDispatcher()
  $: waiting = brief?.waiting || null
</script>

<div class="focus-brief">
  <h2>{brief?.greeting || 'Hello'}.</h2>
  <p>{brief?.sentence || 'Checking what happened while you were away…'}</p>
  {#if waiting}
    <div class="focus-waiting" role="group" aria-label="Waiting on you">
      <p class="waiting-text"><span>{waiting.lead}:</span> {waiting.text}</p>
      {#if waiting.detail}<p class="waiting-detail">{waiting.detail}</p>{/if}
      <div class="waiting-actions">
        <button class="go" disabled={busy} on:click={() => dispatch('decide', { callId: waiting.callId, ok: true })}>Approve</button>
        <button disabled={busy} on:click={() => dispatch('decide', { callId: waiting.callId, ok: false })}>Decline</button>
        <button on:click={() => dispatch('open', 'feed')}>Details</button>
      </div>
      {#if error}<p class="waiting-error" role="alert">{error}</p>{/if}
    </div>
  {/if}
</div>

<style>
  .focus-brief { width: 100%; max-width: 40rem; margin: 0 auto; display: grid; gap: .6rem; text-align: left; }
  h2 { font-size: 1.75rem; font-weight: 700; letter-spacing: -.025em; line-height: 1.2; color: var(--sl-text); }
  p { font-size: 1.05rem; line-height: 1.55; color: var(--sl-text-dim); }
  .focus-waiting { display: grid; gap: .15rem; margin-top: .5rem; }
  .waiting-text { color: var(--sl-text); }
  .waiting-text span { color: var(--sl-text-dim); }
  .waiting-detail { font-size: .82rem; color: var(--sl-text-faint); }
  .waiting-actions { display: flex; gap: 1.1rem; margin-top: .3rem; }
  .waiting-actions button {
    padding: .2rem 0; border: 0; background: transparent; color: var(--sl-text-dim);
    font-size: .92rem; font-weight: 600; cursor: pointer;
  }
  .waiting-actions button.go { color: var(--sl-accent); }
  .waiting-actions button:hover { text-decoration: underline; text-underline-offset: 3px; }
  .waiting-actions button:disabled { opacity: .5; cursor: default; text-decoration: none; }
  .waiting-error { font-size: .85rem; color: #b3394a; }
</style>
