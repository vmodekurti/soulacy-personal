<script>
  export let failure = null
  export let error = ''
</script>

{#if failure || error}
  <section class="run-failure" aria-label="Run failure">
    <strong>{failure?.summary || 'This run failed.'}</strong>
    {#if failure?.step}<div class="step">Step: <code>{failure.step}</code></div>{/if}
    {#if failure?.detail || error}<pre>{failure?.detail || error}</pre>{/if}
    {#if failure?.nextAction}<p><b>What to do:</b> {failure.nextAction}</p>{/if}
    {#if failure?.checks?.length}
      <details>
        <summary>Missing completion checks ({failure.checks.length})</summary>
        <ul>{#each failure.checks as check}<li>{check}</li>{/each}</ul>
      </details>
    {/if}
  </section>
{/if}

<style>
  .run-failure { padding: 14px; margin: 10px 0; color: var(--sl-text); border: 1px solid color-mix(in srgb, var(--danger, #dc6b6b) 45%, transparent); border-radius: 10px; background: color-mix(in srgb, var(--danger, #dc6b6b) 6%, var(--sl-surface)); line-height: 1.5; font-size: 0.875rem; min-width: 0; }
  .step { margin-top: 6px; font-size: 0.9em; overflow-wrap: anywhere; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; margin: 10px 0; max-height: 240px; overflow-y: auto; }
  p { margin: 10px 0 0; }
  details { margin-top: 10px; }
  summary { cursor: pointer; }
  li { overflow-wrap: anywhere; margin: 5px 0; }
</style>
