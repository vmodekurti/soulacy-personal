<script>
  import { createEventDispatcher } from 'svelte'

  export let provider = ''
  export let model = ''
  export let providers = []
  export let models = []
  export let loading = false
  export let error = ''
  export let writable = false
  export let optional = false

  const dispatch = createEventDispatcher()

  $: modelOptions = [...new Set([model, ...models].map((value) => String(value || '').trim()).filter(Boolean))]

  function changeProvider(event) {
    provider = event.currentTarget.value
    model = ''
    dispatch('providerchange', { provider })
  }
</script>
<label>Provider<select value={provider} on:change={changeProvider} disabled={!writable}><option value="">{optional ? 'Inherit default' : 'Deployment default'}</option>{#each providers as id}<option value={id}>{id}</option>{/each}</select></label>
<label>
  <span class="model-head"><span>Model</span>{#if provider}<button type="button" class="refresh" on:click={() => dispatch('refresh', { provider })} disabled={!writable || loading}>{loading ? 'Loading…' : 'Refresh'}</button>{/if}</span>
  <select bind:value={model} disabled={!writable || !provider || loading}>
    <option value="">{optional && !provider ? 'Inherit provider model' : 'Provider default model'}</option>
    {#each modelOptions as id (id)}<option value={id}>{id}</option>{/each}
  </select>
  {#if error}<span class="model-error">Could not discover models: {error}</span>{:else if provider && !loading && modelOptions.length === 0}<span class="model-note">No models were returned by this provider.</span>{/if}
</label>
<style>label{display:grid;gap:6px;color:#cdd1e8;font-size:12px;margin-top:9px}select{width:100%;box-sizing:border-box;background:#15192b;color:#f2f3ff;border:1px solid #303655;border-radius:8px;padding:9px}.model-head{display:flex;align-items:center;justify-content:space-between;gap:10px}.refresh{border:0;background:transparent;color:#9c96ff;padding:0;font-size:11px;cursor:pointer}.refresh:disabled{opacity:.55;cursor:default}.model-error{color:#ff9caf;line-height:1.35}.model-note{color:#9298b5;line-height:1.35}</style>
