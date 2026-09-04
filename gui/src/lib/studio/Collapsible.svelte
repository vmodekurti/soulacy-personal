<script>
  /*
   * Collapsible — a titled editor section that expands/collapses independently
   * and remembers its state across reloads (localStorage, keyed by `id`). Keeps
   * Studio's bottom editor tidy: every section (build report, mocks, assertions,
   * test trace, run trace, history, …) can be folded away on its own.
   *
   * Header actions (Refresh, +Add, ✕) go in the `actions` slot so a click on a
   * button doesn't also toggle the section.
   */
  import { onMount } from 'svelte'

  export let title = ''
  export let sub = ''
  export let open = true
  export let id = '' // localStorage key suffix; empty = not persisted

  onMount(() => {
    if (!id) return
    try {
      const v = localStorage.getItem('studio.section.' + id)
      if (v != null) open = v === '1'
    } catch (_) { /* storage unavailable */ }
  })

  function toggle() {
    open = !open
    if (!id) return
    try { localStorage.setItem('studio.section.' + id, open ? '1' : '0') } catch (_) { /* ignore */ }
  }
</script>

<section class="panel collapsible" class:is-open={open}>
  <header class="collapsible-head">
    <button class="collapsible-toggle" type="button" on:click={toggle} aria-expanded={open}>
      <span class="caret">{open ? '▾' : '▸'}</span>
      <span class="collapsible-title">{title}</span>
      {#if sub}<span class="collapsible-sub">{sub}</span>{/if}
    </button>
    {#if $$slots.actions}
      <div class="collapsible-actions"><slot name="actions" /></div>
    {/if}
  </header>
  {#if open}
    <div class="collapsible-body"><slot /></div>
  {/if}
</section>

<style>
  .collapsible { display: flex; flex-direction: column; }
  .collapsible-head { display: flex; align-items: center; gap: 8px; }
  .collapsible-toggle {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    color: inherit;
  }
  .caret { flex: none; color: var(--text-muted, #8b93ab); font-size: 11px; width: 12px; }
  .collapsible-title { font-weight: 600; font-size: 13px; color: var(--text, #e6e9ef); }
  .collapsible-sub {
    font-size: 12px;
    color: var(--text-muted, #8b93ab);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .collapsible-actions { flex: none; display: flex; align-items: center; gap: 6px; }
  .collapsible-body { margin-top: 10px; }
</style>
