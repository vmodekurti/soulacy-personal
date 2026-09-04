<script>
  /**
   * FilePicker — a single-value textbox with a lookup button.
   *
   * The text input stays fully editable (paths can be pasted or typed);
   * the 📂 button opens a searchable dropdown of known options (e.g. the
   * Python tool catalog) and picking one fills the box.
   *
   * Props:
   *   value        — the current string value (two-way via `change` event).
   *   options      — Array<{value, label?, description?}>.
   *   placeholder  — input placeholder.
   *   disabled     — disables the control.
   *
   * Events:
   *   change — fired with the new string on every edit.
   *   pick   — fired with the chosen option object when the user picks
   *            from the dropdown (lets callers autofill related fields).
   */
  import { createEventDispatcher } from 'svelte'
  import { filterPickerOptions } from './pickerutils.js'

  export let value = ''
  export let options = []
  export let placeholder = ''
  export let disabled = false

  const dispatch = createEventDispatcher()

  let open = false
  let query = ''
  let activeIndex = 0
  let searchEl

  $: filtered = filterPickerOptions(options, query)

  function toggle() {
    if (disabled) return
    open = !open
    if (open) {
      query = ''
      activeIndex = 0
      setTimeout(() => searchEl?.focus(), 0)
    }
  }

  function pick(opt) {
    value = opt.value
    open = false
    dispatch('change', value)
    dispatch('pick', opt)
  }

  function onInput(e) {
    value = e.target.value
    dispatch('change', value)
  }

  function onSearchKey(e) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      activeIndex = Math.min(activeIndex + 1, filtered.length - 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      activeIndex = Math.max(activeIndex - 1, 0)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (filtered[activeIndex]) pick(filtered[activeIndex])
    } else if (e.key === 'Escape') {
      open = false
    }
  }
</script>

<div class="file-picker" class:disabled>
  <div class="row">
    <input
      type="text"
      {placeholder}
      {disabled}
      value={value || ''}
      on:input={onInput}
    />
    {#if (options || []).length > 0}
      <button
        type="button"
        class="browse"
        title="Pick from known files"
        {disabled}
        on:click={toggle}
      >📂 Browse</button>
    {/if}
  </div>

  {#if open}
    <div class="dropdown">
      <input
        type="text"
        class="search"
        bind:this={searchEl}
        bind:value={query}
        on:input={() => activeIndex = 0}
        on:keydown={onSearchKey}
        on:blur={() => setTimeout(() => open = false, 150)}
        placeholder="Filter…"
      />
      {#if filtered.length === 0}
        <div class="empty">No matches</div>
      {:else}
        {#each filtered as opt, i (opt.value)}
          <div
            class="item"
            class:active={i === activeIndex}
            on:mousedown|preventDefault={() => pick(opt)}
            on:mouseenter={() => activeIndex = i}
            role="option"
            tabindex="-1"
            aria-selected={i === activeIndex}
          >
            <span class="item-label">{opt.label || opt.value}</span>
            <span class="item-value">{opt.value}</span>
            {#if opt.description}
              <span class="item-desc">{opt.description}</span>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<style>
  .file-picker { position: relative; }
  .file-picker.disabled { opacity: .55; }

  .row { display: flex; gap: .4rem; align-items: stretch; }
  .row input[type="text"] {
    flex: 1;
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .85rem; padding: .45rem .6rem;
    font-family: monospace;
  }
  .row input[type="text"]:focus { outline: none; border-color: #6c63ff; }

  .browse {
    background: rgba(108,99,255,.14); border: 1px solid rgba(108,99,255,.45);
    color: #ada8ff; border-radius: 6px; cursor: pointer;
    font-size: .78rem; padding: 0 .7rem; white-space: nowrap;
  }
  .browse:hover { background: rgba(108,99,255,.25); }

  .dropdown {
    position: absolute; top: calc(100% + 4px); left: 0; right: 0; z-index: 60;
    background: #141626; border: 1px solid #2a2f4a; border-radius: 8px;
    max-height: 300px; overflow-y: auto;
    box-shadow: 0 8px 24px rgba(0,0,0,.45);
    padding: .35rem;
  }
  .search {
    width: 100%; box-sizing: border-box; margin-bottom: .3rem;
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .8rem; padding: .35rem .5rem;
  }
  .search:focus { outline: none; border-color: #6c63ff; }

  .empty { padding: .5rem .6rem; color: #6b7294; font-size: .8rem; }

  .item {
    padding: .4rem .55rem; border-radius: 6px; cursor: pointer;
    display: flex; flex-direction: column; gap: .1rem;
  }
  .item:hover, .item.active { background: rgba(108,99,255,.12); }
  .item-label { font-family: monospace; color: #8b85ff; font-size: .82rem; }
  .item-value { color: #9aa0c0; font-size: .72rem; font-family: monospace; }
  .item-desc  { color: #6b7294; font-size: .72rem; }
</style>
