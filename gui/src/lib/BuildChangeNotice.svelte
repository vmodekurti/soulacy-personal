<script>
  // Says out loud when the runtime changed under this page: a new build (with
  // the old and new names side by side) or a restart of the same build.
  import { onDestroy } from 'svelte'
  import { buildChange } from './buildwatch.js'

  let timer

  $: change = $buildChange
  // A restart, or an update this page already loaded, needs no action from the
  // operator: show it, then step out of the way. An update that left this page
  // on the old interface stays until it is reloaded or dismissed.
  $: {
    clearTimeout(timer)
    if (change && !change.reloadNeeded) timer = setTimeout(dismiss, change.kind === 'restarted' ? 8000 : 20000)
  }

  function dismiss() { buildChange.set(null) }
  function reload() { location.reload() }

  onDestroy(() => clearTimeout(timer))
</script>

{#if change}
  <div class="notice" class:update={change.kind === 'updated'} role="status" aria-live="polite" data-testid="build-change-notice">
    <span class="icon" aria-hidden="true">{change.kind === 'updated' ? '✦' : '↻'}</span>
    <p>
      {#if change.kind === 'updated'}
        <strong>Soulacy is running a new build: <code>{change.to.label}</code></strong>
        <span>Previously <code>{change.from.label}</code>.
          {#if change.reloadNeeded}Reload to load the matching interface.{/if}</span>
      {:else}
        <strong>The runtime restarted</strong>
        <span>Still on build <code>{change.to.label}</code>.</span>
      {/if}
    </p>
    {#if change.reloadNeeded}<button class="primary" on:click={reload}>Reload now</button>{/if}
    <button class="ghost" on:click={dismiss} aria-label="Dismiss build notice">Dismiss</button>
  </div>
{/if}

<style>
  .notice {
    display: flex; align-items: center; gap: .75rem; flex-wrap: wrap;
    margin: 0 0 .9rem; padding: .7rem .9rem; border-radius: 12px;
    border: 1px solid var(--sl-line); background: var(--sl-surface); color: var(--sl-text);
  }
  .notice.update { border-color: var(--sl-accent); background: var(--sl-accent-soft); }
  .icon { font-size: 1rem; color: var(--sl-accent); }
  p { flex: 1 1 220px; margin: 0; display: flex; flex-direction: column; gap: .1rem; font-size: .8rem; }
  p span { color: var(--sl-text-dim); }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .78rem; }
  button { font: inherit; font-size: .75rem; font-weight: 650; padding: .4rem .75rem; border-radius: 8px; cursor: pointer; }
  .primary { border: 1px solid var(--sl-accent); background: var(--sl-accent-fill, var(--sl-accent)); color: var(--sl-on-accent, #fff); }
  .ghost { border: 1px solid var(--sl-line); background: transparent; color: var(--sl-text-dim); }
</style>
