<script>
  /**
   * ChipPicker — a typeahead + multi-select chip input.
   *
   * Props:
   *   value          — array of selected IDs (string[]). For single mode, an
   *                    array of length 0 or 1; we keep the same shape so the
   *                    caller doesn't have to branch.
   *   options        — Array<{value, label, description?, group?}>. group is an
   *                    optional category label that, when present, splits the
   *                    suggestions list into <ul> sections per group.
   *   placeholder    — text shown when the input is empty.
   *   single         — true = single-select (selecting a chip replaces the
   *                    existing one). false = multi-select.
   *   allowFreeform  — true = allow values not in options (e.g. for fields
   *                    that may legitimately reference unknown items). false
   *                    = only options are addable.
   *   placement      — "down" (default) or "up" for the suggestions menu.
   *   disabled       — disables the entire control.
   *
   * Events: dispatches `change` with the new value[] whenever value changes.
   */
  import { createEventDispatcher, tick } from 'svelte'

  export let value = []
  export let options = []
  export let placeholder = ''
  export let single = false
  export let allowFreeform = false
  export let placement = 'down'
  export let disabled = false

  const dispatch = createEventDispatcher()

  let inputText = ''
  let showSuggestions = false
  let activeIndex = 0
  let inputEl
  let containerEl

  $: lcInput = inputText.trim().toLowerCase()
  $: filteredOptions = options.filter(o =>
    !value.includes(o.value) &&
    (lcInput === '' || (o.label || o.value).toLowerCase().includes(lcInput) ||
                       (o.description || '').toLowerCase().includes(lcInput))
  )
  // When the input has a freeform value not in options, prepend a synthetic
  // option so the user sees it as the first suggestion and can hit Enter.
  $: suggestions = (() => {
    if (!allowFreeform || lcInput === '') return filteredOptions
    const exists = options.some(o => o.value.toLowerCase() === lcInput)
    if (exists) return filteredOptions
    return [{ value: inputText.trim(), label: inputText.trim(), description: '(custom)' }, ...filteredOptions]
  })()
  $: groupedSuggestions = groupBy(suggestions, s => s.group || '')

  function groupBy(arr, keyFn) {
    const map = new Map()
    for (const item of arr) {
      const k = keyFn(item)
      if (!map.has(k)) map.set(k, [])
      map.get(k).push(item)
    }
    return [...map.entries()]
  }

  function labelFor(v) {
    const opt = options.find(o => o.value === v)
    return opt ? opt.label : v
  }
  function descFor(v) {
    const opt = options.find(o => o.value === v)
    return opt ? (opt.description || '') : ''
  }

  function addChip(v) {
    if (disabled) return
    if (single) {
      value = [v]
    } else if (!value.includes(v)) {
      value = [...value, v]
    }
    inputText = ''
    activeIndex = 0
    showSuggestions = false
    dispatch('change', value)
  }
  function removeChip(v) {
    if (disabled) return
    value = value.filter(x => x !== v)
    dispatch('change', value)
  }

  function onKey(e) {
    if (e.key === 'Backspace' && inputText === '' && value.length > 0) {
      removeChip(value[value.length - 1])
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      showSuggestions = true
      activeIndex = Math.min(activeIndex + 1, suggestions.length - 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      activeIndex = Math.max(activeIndex - 1, 0)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const pick = suggestions[activeIndex]
      if (pick) addChip(pick.value)
      else if (allowFreeform && inputText.trim()) addChip(inputText.trim())
    } else if (e.key === 'Escape') {
      showSuggestions = false
    }
  }

  function onContainerClick() {
    if (disabled) return
    inputEl?.focus()
    showSuggestions = true
  }

  // Lookup button: browse the full option list without typing.
  function toggleBrowse(e) {
    e.stopPropagation()
    if (disabled) return
    if (showSuggestions) {
      showSuggestions = false
    } else {
      inputText = ''
      activeIndex = 0
      showSuggestions = true
      inputEl?.focus()
    }
  }
</script>

<div
  class="chip-picker"
  class:disabled
  class:open-up={placement === 'up'}
  bind:this={containerEl}
  on:click={onContainerClick}
  on:keydown={(e) => e.key === 'Enter' && onContainerClick()}
  role="combobox"
  tabindex="-1"
  aria-expanded={showSuggestions}
  aria-controls="chip-picker-suggestions"
>
  <div class="chips-row">
    {#each value as v (v)}
      <span class="chip">
        <span class="chip-label" title={descFor(v)}>{labelFor(v)}</span>
        {#if !disabled}
          <button type="button" class="chip-x" on:click|stopPropagation={() => removeChip(v)}>×</button>
        {/if}
      </span>
    {/each}
    {#if !disabled && (!single || value.length === 0)}
      <input
        type="text"
        bind:this={inputEl}
        bind:value={inputText}
        on:focus={() => { showSuggestions = true; activeIndex = 0 }}
        on:blur={() => setTimeout(() => showSuggestions = false, 150)}
        on:keydown={onKey}
        on:input={() => { showSuggestions = true; activeIndex = 0 }}
        placeholder={value.length === 0 ? placeholder : ''}
      />
    {/if}
    {#if !disabled && options.length > 0}
      <button
        type="button"
        class="browse-btn"
        title="Browse all options"
        aria-label="Browse all options"
        on:click={toggleBrowse}
        on:mousedown|preventDefault|stopPropagation
      >▾</button>
    {/if}
  </div>

  {#if showSuggestions && suggestions.length > 0}
    <div class="suggestions" id="chip-picker-suggestions">
      {#each groupedSuggestions as [groupName, items]}
        {#if groupName}
          <div class="suggestion-group">{groupName}</div>
        {/if}
        {#each items as s, i (s.value)}
          {@const flatIdx = suggestions.indexOf(s)}
          <div
            class="suggestion"
            class:active={flatIdx === activeIndex}
            on:mousedown|preventDefault={() => addChip(s.value)}
            on:mouseenter={() => activeIndex = flatIdx}
            role="option"
            tabindex="-1"
            aria-selected={flatIdx === activeIndex}
          >
            <span class="suggestion-label">{s.label}</span>
            {#if s.description}
              <span class="suggestion-desc">{s.description}</span>
            {/if}
          </div>
        {/each}
      {/each}
    </div>
  {/if}
</div>

<style>
  .chip-picker {
    position: relative;
    background: #0e1020;
    border: 1px solid #2a2f4a;
    border-radius: 6px;
    min-height: 2.2rem;
    cursor: text;
    transition: border-color .15s;
  }
  .chip-picker:focus-within { border-color: #6c63ff; box-shadow: 0 0 0 2px rgba(108, 99, 255, 0.15); }
  .chip-picker.disabled { opacity: .55; cursor: not-allowed; }

  .chips-row {
    display: flex; flex-wrap: wrap; align-items: center; gap: .35rem;
    padding: .35rem .5rem;
    min-height: 1.5rem;
  }
  .chip {
    display: inline-flex; align-items: center; gap: .25rem;
    background: rgba(108,99,255,.18); border: 1px solid rgba(108,99,255,.4);
    color: #c8cadf; font-size: .8rem;
    padding: .12rem .5rem .12rem .55rem;
    border-radius: 999px; font-family: monospace;
  }
  .chip-label { white-space: nowrap; }
  .chip-x {
    background: none; border: none; color: #8b85ff;
    cursor: pointer; padding: 0 0 0 .15rem; font-size: 1rem; line-height: 1;
  }
  .chip-x:hover { color: #f06060; }

  .chips-row input[type="text"] {
    flex: 1; min-width: 8rem;
    background: transparent; border: none; outline: none;
    color: #e8eaf6; font-size: .85rem; padding: .15rem 0;
  }

  .browse-btn {
    margin-left: auto; flex-shrink: 0;
    background: rgba(108,99,255,.14); border: 1px solid rgba(108,99,255,.45);
    color: #ada8ff; border-radius: 6px; cursor: pointer;
    font-size: .8rem; line-height: 1; padding: .25rem .5rem;
  }
  .browse-btn:hover { background: rgba(108,99,255,.25); }

  .suggestions {
    position: absolute; top: calc(100% + 4px); left: 0; right: 0; z-index: 50;
    background: #141626; border: 1px solid #2a2f4a; border-radius: 8px;
    max-height: 280px; overflow-y: auto;
    box-shadow: 0 8px 24px rgba(0,0,0,.45);
  }
  .chip-picker.open-up .suggestions {
    top: auto;
    bottom: calc(100% + 4px);
    box-shadow: 0 -8px 24px rgba(0,0,0,.45);
  }
  .suggestion-group {
    padding: .35rem .7rem .2rem; font-size: .65rem;
    color: #555a7a; text-transform: uppercase; letter-spacing: .08em; font-weight: 600;
  }
  .suggestion {
    padding: .45rem .7rem; cursor: pointer;
    display: flex; align-items: baseline; gap: .6rem;
    font-size: .82rem; color: #c8cadf;
  }
  .suggestion:hover, .suggestion.active { background: rgba(108,99,255,.12); }
  .suggestion-label { font-family: monospace; color: #8b85ff; flex-shrink: 0; }
  .suggestion-desc { color: #6b7294; font-size: .75rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
