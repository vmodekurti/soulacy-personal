<script>
  export let advanced = false
  export let description = 'Everyday covers AI, search, and memory. Advanced adds execution limits and server settings. Switching views keeps your changes.'
</script>

<div class="settings-mode">
  <div class="choices" role="group" aria-label="Settings level">
    <button type="button" aria-pressed={!advanced} on:click={() => advanced = false}>Everyday</button>
    <button type="button" aria-pressed={advanced} on:click={() => advanced = true}>Advanced</button>
  </div>
  <p>{description}</p>
</div>

<style>
  .settings-mode { display: flex; flex-direction: column; gap: .6rem; }
  .choices { display: flex; gap: .25rem; padding: .25rem; border: 1px solid var(--sl-line); border-radius: 9px; width: fit-content; max-width: 100%; }
  button { background: transparent; color: var(--sl-text-dim); border: 0; border-radius: 6px; padding: .5rem 1rem; font: inherit; font-size: .85rem; cursor: pointer; }
  button[aria-pressed="true"] { background: var(--sl-surface); color: var(--sl-accent-hover, #b0a8ff); box-shadow: 0 1px 4px #0002; }
  button:focus-visible { outline: 2px solid var(--sl-accent-hover, #b0a8ff); outline-offset: 2px; }
  p { margin: 0; color: var(--sl-text-dim); font-size: .8rem; line-height: 1.5; }
</style>
