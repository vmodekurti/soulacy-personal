<script>
  /**
   * KeyValueEditor — small editor for map<string,string> values like env vars,
   * HTTP headers, or arbitrary metadata. Two columns of inputs + Add/Remove
   * buttons. Dispatches `change` whenever the underlying object mutates.
   *
   * Props:
   *   value        — { [key]: value } object. Reactive: the parent's binding
   *                  receives an entirely new object on each change so Svelte's
   *                  reactivity triggers reliably.
   *   keyLabel     — column header for the key (default "Key").
   *   valueLabel   — column header for the value (default "Value").
   *   keyPlaceholder
   *   valuePlaceholder
   *   maskValues   — when true, value inputs render as type=password. Useful
   *                  for secrets like API tokens.
   *   disabled
   */
  import { createEventDispatcher } from 'svelte'

  export let value = {}
  export let keyLabel = 'Key'
  export let valueLabel = 'Value'
  export let keyPlaceholder = 'NAME'
  export let valuePlaceholder = 'value'
  export let maskValues = false
  export let disabled = false

  const dispatch = createEventDispatcher()

  // Internal model: an array of [key, value] tuples so the user can have a
  // partially-typed row without it being committed to `value` yet. The keys
  // can also temporarily collide while typing — we dedupe on emit.
  let rows = []

  // Sync `value` → `rows` only on prop change. We avoid feedback loops by
  // tracking the last-emitted object.
  let lastEmitted = {}
  $: if (value !== lastEmitted) {
    rows = Object.entries(value || {}).map(([k, v]) => ({ k, v: String(v ?? '') }))
    if (rows.length === 0) rows = [{ k: '', v: '' }]
  }

  function emit() {
    const out = {}
    for (const { k, v } of rows) {
      const key = k.trim()
      if (key === '') continue
      out[key] = v
    }
    lastEmitted = out
    value = out
    dispatch('change', out)
  }

  function addRow() {
    rows = [...rows, { k: '', v: '' }]
  }
  function removeRow(i) {
    rows = rows.filter((_, j) => j !== i)
    if (rows.length === 0) rows = [{ k: '', v: '' }]
    emit()
  }
  function onKeyEdit(i, val) { rows[i].k = val; emit() }
  function onValEdit(i, val) { rows[i].v = val; emit() }
</script>

<div class="kv-editor" class:disabled>
  <div class="kv-header">
    <span>{keyLabel}</span>
    <span>{valueLabel}</span>
    <span></span>
  </div>
  {#each rows as row, i (i)}
    <div class="kv-row">
      <input
        type="text"
        placeholder={keyPlaceholder}
        value={row.k}
        on:input={(e) => onKeyEdit(i, e.target.value)}
        {disabled}
      />
      <input
        type={maskValues ? 'password' : 'text'}
        placeholder={valuePlaceholder}
        value={row.v}
        on:input={(e) => onValEdit(i, e.target.value)}
        {disabled}
      />
      <button
        type="button"
        class="kv-remove"
        title="Remove row"
        on:click={() => removeRow(i)}
        {disabled}
      >×</button>
    </div>
  {/each}
  <button type="button" class="kv-add" on:click={addRow} {disabled}>+ Add</button>
</div>

<style>
  .kv-editor { display: flex; flex-direction: column; gap: .35rem; }
  .kv-editor.disabled { opacity: .5; pointer-events: none; }

  .kv-header {
    display: grid; grid-template-columns: 1fr 1fr 2rem; gap: .5rem;
    font-size: .65rem; color: #555a7a; text-transform: uppercase;
    letter-spacing: .08em; font-weight: 600; padding: 0 .15rem;
  }
  .kv-row {
    display: grid; grid-template-columns: 1fr 1fr 2rem; gap: .5rem;
    align-items: center;
  }
  .kv-row input {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .82rem; padding: .35rem .55rem;
    font-family: monospace;
  }
  .kv-row input:focus { border-color: #6c63ff; outline: none; box-shadow: 0 0 0 2px rgba(108,99,255,.15); }
  .kv-remove {
    background: none; border: 1px solid rgba(240,96,96,.3); color: #f06060;
    border-radius: 6px; cursor: pointer; font-size: 1rem; line-height: 1;
    padding: 0 .35rem; height: 1.85rem;
  }
  .kv-remove:hover { background: rgba(240,96,96,.1); }

  .kv-add {
    align-self: flex-start; background: rgba(108,99,255,.12); color: #8b85ff;
    border: 1px solid rgba(108,99,255,.35); padding: .25rem .6rem;
    border-radius: 6px; font-size: .72rem; font-weight: 600; cursor: pointer;
    margin-top: .15rem;
  }
  .kv-add:hover { background: rgba(108,99,255,.2); }
</style>
