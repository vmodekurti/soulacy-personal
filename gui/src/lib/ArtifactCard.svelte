<script>
  // A file the work produced, shown as something to open rather than a link to
  // find. Renders a link when there is a URL and a button when opening it needs
  // the page to fetch it first.
  export let title = ''
  export let subtitle = ''
  export let href = ''
</script>

{#if href}
  <a class="artifact-card" {href} target="_blank" rel="noopener" download aria-label="Open {title}">
    <span class="artifact-card-icon" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/></svg>
    </span>
    <span class="artifact-card-text"><strong>{title}</strong><small>{subtitle}</small></span>
    <span class="artifact-card-go" aria-hidden="true">→</span>
  </a>
{:else}
  <button type="button" class="artifact-card" on:click aria-label="Open {title}">
    <span class="artifact-card-icon" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/></svg>
    </span>
    <span class="artifact-card-text"><strong>{title}</strong><small>{subtitle}</small></span>
    <span class="artifact-card-go" aria-hidden="true">→</span>
  </button>
{/if}

<style>
  .artifact-card {
    width: 100%; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: .9rem;
    padding: .8rem 1.1rem .8rem .9rem; border: 1px solid var(--sl-line); border-radius: 14px;
    color: var(--sl-text); background: var(--sl-surface); text-align: left; text-decoration: none; cursor: pointer;
  }
  .artifact-card:hover { border-color: color-mix(in srgb, var(--sl-accent) 40%, var(--sl-line)); }
  .artifact-card-icon { display: grid; place-items: center; padding-right: .9rem; border-right: 1px solid var(--sl-line); color: var(--sl-accent-ink); }
  .artifact-card-text { min-width: 0; display: grid; gap: .1rem; }
  .artifact-card-text strong { overflow: hidden; font-size: .98rem; font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
  .artifact-card-text small { color: var(--sl-text-dim); font-size: .8rem; }
  .artifact-card-go { color: var(--sl-text); font-size: 1.2rem; }
</style>
