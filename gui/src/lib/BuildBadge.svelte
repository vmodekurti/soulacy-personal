<script>
  // Always-visible "which build is running" pill. The codename changes with
  // every new binary, so after a redeploy the operator only has to glance here.
  import { onMount, onDestroy } from 'svelte'
  import { runtimeBuild, buildChange, isFreshStart, agoLabel, commitLabel, buildDetailsText } from './buildwatch.js'

  export let compact = false

  let open = false
  let copied = false
  let now = Date.now()
  let tick
  let root

  onMount(() => { tick = setInterval(() => { now = Date.now() }, 30000) })
  onDestroy(() => clearInterval(tick))

  $: build = $runtimeBuild
  $: justChanged = $buildChange && $buildChange.kind === 'updated'
  $: fresh = build && (justChanged || isFreshStart(build, now))
  $: title = build
    ? `Running build ${build.label} · started ${agoLabel(build.started_at, now)}`
    : 'Waiting for the runtime to report its build'

  async function copy() {
    try {
      await navigator.clipboard.writeText(buildDetailsText(build, Date.now()))
      copied = true
      setTimeout(() => { copied = false }, 1500)
    } catch {
      copied = false
    }
  }

  function onWindowClick(e) {
    if (open && root && !root.contains(e.target)) open = false
  }
</script>

<svelte:window on:click={onWindowClick} on:keydown={(e) => e.key === 'Escape' && (open = false)} />

<div class="build" bind:this={root}>
  <button class="pill" class:fresh class:compact on:click={() => (open = !open)}
          aria-expanded={open} aria-haspopup="dialog" title={title}
          data-testid="build-badge">
    <span class="dot" aria-hidden="true"></span>
    {#if build}
      <span class="label">{build.label}</span>
      {#if fresh && !compact}<span class="new">new</span>{/if}
    {:else}
      <span class="label muted">build…</span>
    {/if}
  </button>

  {#if open}
    <div class="pop" role="dialog" aria-label="Runtime build details">
      {#if build}
        <p class="eyebrow">Runtime build</p>
        <p class="name">{build.label}</p>
        <p class="sub">
          {#if fresh}Deployed {agoLabel(build.started_at, now)}{:else}Running since {agoLabel(build.started_at, now)}{/if}
        </p>
        <dl>
          <dt>Build ID</dt><dd><code>{build.build_id}</code></dd>
          <dt>Version</dt><dd>{build.version || 'unknown'}</dd>
          <dt>Commit</dt><dd>{commitLabel(build)}</dd>
          {#if build.built_at}<dt>Built</dt><dd>{new Date(build.built_at).toLocaleString()} · {agoLabel(build.built_at, now)}</dd>{/if}
          <dt>Started</dt><dd>{new Date(build.started_at).toLocaleString()}</dd>
          <dt>Instance</dt><dd><code>{build.instance_id}</code></dd>
        </dl>
        <p class="hint">The name changes every time new code is deployed. A restart keeps the name and changes the instance.</p>
        <button class="copy" on:click={copy}>{copied ? 'Copied' : 'Copy details'}</button>
      {:else}
        <p class="sub">The runtime has not reported a build yet. It may be restarting or predate build labels.</p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .build { position: relative; }
  .pill {
    display: inline-flex; align-items: center; gap: .4rem;
    padding: .32rem .6rem; border-radius: 999px;
    border: 1px solid var(--sl-line); background: var(--sl-surface);
    color: var(--sl-text-dim); font: inherit; font-size: .7rem; font-weight: 650;
    cursor: pointer; white-space: nowrap;
  }
  .pill:hover { border-color: var(--sl-accent); color: var(--sl-text); }
  .pill.compact { padding: .25rem .5rem; }
  .dot { width: 7px; height: 7px; border-radius: 2px; transform: rotate(45deg); background: var(--sl-text-faint); }
  .pill.fresh { border-color: var(--sl-accent); color: var(--sl-text); background: var(--sl-accent-soft); }
  .pill.fresh .dot { background: var(--sl-accent); }
  .label { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: .01em; }
  .muted { color: var(--sl-text-faint); }
  .new {
    font-size: .58rem; text-transform: uppercase; letter-spacing: .06em;
    padding: .05rem .35rem; border-radius: 999px;
    background: var(--sl-accent); color: var(--sl-on-accent, #fff);
  }
  .pop {
    position: absolute; right: 0; top: calc(100% + .45rem); z-index: 60;
    width: min(320px, calc(100vw - 32px));
    padding: .9rem 1rem; border-radius: 12px;
    border: 1px solid var(--sl-line); background: var(--sl-surface-raised, var(--sl-surface));
    box-shadow: var(--sl-shadow-sm, 0 8px 24px rgba(0,0,0,.18)); color: var(--sl-text);
    text-align: left;
  }
  .eyebrow { margin: 0; font-size: .62rem; text-transform: uppercase; letter-spacing: .08em; color: var(--sl-text-faint); }
  .name { margin: .15rem 0 0; font-size: 1.15rem; font-weight: 700; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .sub { margin: .15rem 0 .6rem; font-size: .75rem; color: var(--sl-text-dim); }
  dl { display: grid; grid-template-columns: auto 1fr; gap: .3rem .75rem; margin: 0 0 .6rem; font-size: .72rem; }
  dt { color: var(--sl-text-faint); }
  dd { margin: 0; color: var(--sl-text); overflow-wrap: anywhere; }
  code { font-size: .7rem; }
  .hint { margin: 0 0 .6rem; font-size: .68rem; color: var(--sl-text-faint); line-height: 1.4; }
  .copy {
    font: inherit; font-size: .72rem; font-weight: 650; padding: .35rem .7rem; border-radius: 8px;
    border: 1px solid var(--sl-line); background: var(--sl-surface); color: var(--sl-text); cursor: pointer;
  }
  .copy:hover { border-color: var(--sl-accent); }
</style>
