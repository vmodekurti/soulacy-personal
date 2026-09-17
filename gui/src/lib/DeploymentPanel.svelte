<script>
  // What this deployment can and cannot do.
  //
  // Installing something that needs a shell, on a platform where the user has
  // no shell, fails at a step nobody warned them about — and the alternative,
  // bundling every heavy dependency into the image on the chance somebody
  // wants it, makes every install pay for what few of them use. So the limits
  // are stated, each with the way around it.
  //
  // Limits lead. A deployment that can do everything says so in one line and
  // gets out of the way; a deployment that cannot puts the reason, and the
  // remedy, in front of the person about to hit it.
  import { limitsOf, summaryOf } from './deployment.js'

  export let report = null
  let open = false

  $: limits = limitsOf(report)
  $: summary = summaryOf(report)
</script>

{#if report}
  <div class="deployment" class:has-limits={limits.length > 0}>
    <button class="head" on:click={() => (open = !open)} aria-expanded={open}>
      <span class="what">
        {#if limits.length === 0}
          This deployment can do everything Soulacy offers
        {:else}
          {limits.length} thing{limits.length === 1 ? '' : 's'} this deployment cannot do
        {/if}
      </span>
      <span class="summary">{summary}</span>
      <span class="chev" aria-hidden="true">{open ? '▾' : '▸'}</span>
    </button>

    {#if limits.length > 0}
      <ul class="limits">
        {#each limits as c (c.id)}
          <li>
            <span class="name">{c.name}</span>
            <span class="detail">{c.detail}</span>
            {#if c.workaround}<span class="fix">Instead: {c.workaround}</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if open}
      <ul class="all">
        {#each report.capabilities as c (c.id)}
          <li class:ok={c.available}>
            <span class="mark" aria-hidden="true">{c.available ? '✓' : '✕'}</span>
            <span class="name">{c.name}</span>
            <span class="detail">{c.detail}</span>
          </li>
        {/each}
      </ul>
      {#if report.workspace}
        <p class="ws">Installs land in <code>{report.workspace}</code></p>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .deployment {
    border: 1px solid var(--sl-line);
    border-radius: var(--sl-radius-lg, 12px);
    background: var(--sl-surface);
    margin-bottom: 1rem;
    overflow: hidden;
  }
  .deployment.has-limits { border-color: color-mix(in srgb, orange 35%, var(--sl-line)); }
  .head {
    display: flex; align-items: center; gap: .6rem; width: 100%;
    padding: .65rem .9rem; background: none; border: 0; cursor: pointer;
    color: var(--sl-text); font: inherit; text-align: left;
  }
  .what { font-weight: 600; font-size: .85rem; }
  .summary { color: var(--sl-text-faint); font-size: .78rem; margin-left: auto; }
  .chev { color: var(--sl-text-faint); font-size: .75rem; }
  ul { list-style: none; margin: 0; padding: 0 .9rem .7rem; }
  .limits li { padding: .45rem 0; border-top: 1px solid var(--sl-line); display: grid; gap: .15rem; }
  .limits .name { font-size: .82rem; font-weight: 600; }
  .limits .detail, .all .detail { font-size: .78rem; color: var(--sl-text-dim); }
  .fix { font-size: .78rem; color: var(--sl-text); }
  .all li { display: grid; grid-template-columns: 1.1rem 1fr; gap: .1rem .4rem; padding: .3rem 0; }
  .all .name { grid-column: 2; font-size: .8rem; }
  .all .detail { grid-column: 2; }
  .all .mark { grid-row: 1 / span 2; color: var(--sl-text-faint); }
  .all li.ok .mark { color: var(--sl-accent); }
  .ws { margin: 0; padding: 0 .9rem .7rem; font-size: .75rem; color: var(--sl-text-faint); }
  code { background: rgba(0,0,0,.2); padding: .05rem .3rem; border-radius: 4px; }
</style>
