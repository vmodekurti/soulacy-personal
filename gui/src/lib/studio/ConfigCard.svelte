<script>
  // ConfigCard — edit a block through structured fields instead of raw YAML/JSON
  // (Guided Studio Builder, Story 9). Missing required fields are highlighted and
  // a suggested default can be applied in one click. Edits are written back to
  // the node via onUpdate (which the parent patches into the workflow).
  import { configFields, applyField } from './configfields.js'

  export let node = null
  export let onUpdate = null    // (updatedNode) => void

  $: fields = node ? configFields(node) : []

  function set(field, value) {
    if (onUpdate && node) onUpdate(applyField(node, field, value))
  }
</script>

<!-- Text fields commit on `change` (blur / Enter), not `input`: every commit
     replaces the parent's workflow object, which re-lanes this card and can
     destroy the focused element mid-word, and fires a validate round-trip. -->
{#if node}
  <div class="config-card">
    {#each fields as f (f.key)}
      <label class="cf-field" class:missing={f.required && f.missing}>
        <span class="cf-label">
          {f.label}{#if f.required}<span class="cf-req" title="Required">*</span>{/if}
          {#if f.required && f.missing}<span class="cf-missing">required</span>{/if}
        </span>
        {#if f.type === 'select'}
          <select value={f.value} on:change={(e) => set(f, e.target.value)}>
            <option value="" disabled>Choose…</option>
            {#each f.options as opt}<option value={opt}>{opt}</option>{/each}
          </select>
        {:else if f.type === 'code'}
          <textarea class="cf-code" rows="6" value={f.value} on:change={(e) => set(f, e.target.value)}
                    placeholder="def run(inputs):&#10;    return inputs"></textarea>
        {:else}
          <input type="text" value={f.value} on:change={(e) => set(f, e.target.value)}
                 placeholder={f.suggestion || ''} />
        {/if}
        {#if f.suggestion && !f.value}
          <button type="button" class="cf-suggest" on:click={() => set(f, f.suggestion)}
                  title="Use suggested default">Use “{f.suggestion}”</button>
        {/if}
      </label>
    {/each}
  </div>
{/if}

<style>
  .config-card { display: flex; flex-direction: column; gap: 10px; margin-top: 8px; padding: 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-elev); }
  .cf-field { display: flex; flex-direction: column; gap: 4px; }
  .cf-label { font-size: 11px; color: var(--text-muted); display: flex; align-items: center; gap: 6px; }
  .cf-req { color: var(--error, #ff6b81); }
  .cf-missing { font-size: 10px; padding: 0 6px; border-radius: 999px; background: rgba(255,107,129,.16); color: #ff6b81; }
  .cf-field.missing input, .cf-field.missing select, .cf-field.missing textarea { border-color: #ff6b81; }
  .cf-field input, .cf-field select, .cf-field textarea {
    background: var(--bg-elev-2); border: 1px solid var(--border); border-radius: 6px;
    padding: 6px 8px; color: var(--text); font-size: 12px; width: 100%; box-sizing: border-box;
  }
  .cf-code { font-family: ui-monospace, Menlo, monospace; resize: vertical; }
  .cf-suggest { align-self: flex-start; background: none; border: none; color: var(--accent, #6c8cff); font-size: 11px; cursor: pointer; padding: 0; }
</style>
