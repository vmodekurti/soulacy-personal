<script>
  // WizardRail — the Describe › Build › Test › Save step rail.
  //
  // Makes the order of operations explicit. Studio previously offered a canvas
  // with tabs, so nothing conveyed that describing precedes building, or that
  // you had never tested the thing you were about to deploy.
  //
  // A blocked step is rendered as a disabled button carrying its reason rather
  // than being hidden. A step that silently does not respond is indistinguishable
  // from a broken one, and hiding it removes the explanation of what to do next.

  export let steps = []       // from stepStates()
  export let onGo = () => {}

  function label(s) {
    if (s.status === 'done') return `${s.label} — done`
    if (s.status === 'blocked') return `${s.label} — ${s.reason}`
    return s.label
  }
</script>

<nav class="wr" aria-label="Studio steps">
  {#each steps as s, i (s.id)}
    <button
      class="wr-step {s.status}"
      type="button"
      disabled={!s.enterable}
      aria-current={s.status === 'active' ? 'step' : undefined}
      title={label(s)}
      data-tooltip={s.status === 'blocked' ? s.reason : ''}
      on:click={() => s.enterable && onGo(s.id)}
    >
      <span class="wr-num" aria-hidden="true">
        {#if s.status === 'done'}✓{:else}{s.index}{/if}
      </span>
      <span class="wr-label">{s.label}</span>
    </button>
    {#if i < steps.length - 1}
      <span class="wr-sep" aria-hidden="true">›</span>
    {/if}
  {/each}
</nav>

<style>
  .wr { display: flex; align-items: center; gap: 2px; flex-wrap: wrap; }

  .wr-step {
    display: inline-flex; align-items: center; gap: 7px;
    padding: 5px 12px; border-radius: 999px;
    background: transparent; border: 1px solid transparent;
    font-size: .84rem; color: var(--text-dim, #6b7294); cursor: pointer;
  }
  .wr-step:hover:not(:disabled) { background: color-mix(in srgb, var(--text-dim, #6b7294) 10%, transparent); }
  .wr-step:disabled { cursor: not-allowed; opacity: .5; }

  .wr-num {
    width: 20px; height: 20px; border-radius: 50%; flex: none;
    display: grid; place-items: center; font-size: .72rem;
    background: color-mix(in srgb, var(--text-dim, #6b7294) 22%, transparent);
    color: var(--text, inherit);
  }

  .wr-step.active {
    color: var(--text, inherit);
    background: color-mix(in srgb, var(--accent, #6d5efc) 14%, transparent);
    border-color: color-mix(in srgb, var(--accent, #6d5efc) 38%, transparent);
  }
  .wr-step.active .wr-num { background: var(--accent, #6d5efc); color: #fff; }

  .wr-step.done { color: var(--text, inherit); }
  .wr-step.done .wr-num {
    background: color-mix(in srgb, var(--ok, #2ea043) 26%, transparent);
    color: var(--ok, #2ea043);
  }

  .wr-sep { color: var(--text-dim, #6b7294); font-size: .8rem; }

  @media (max-width: 720px) {
    /* Numbers alone still convey position; labels are what overflow first. */
    .wr-label { display: none; }
    .wr-step { padding: 5px 8px; }
  }
</style>
