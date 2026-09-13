<script>
  import { onMount, onDestroy } from 'svelte'
  import { apiFetch } from './api.js'
  import { apiKey } from './stores.js'
  import { validatePreparation, supportLabel, strategyLabel } from './modelPreparation.js'
  export let agentID
  let result = null, busy = false, error = '', generation = 0
  function clear() { generation++; result = null; error = ''; busy = false }
  async function load() {
    if (busy || !agentID) return
    clear(); const current = generation; busy = true
    try {
      const value = await apiFetch(`/agents/${encodeURIComponent(agentID)}/model-preparation`)
      if (current === generation) result = validatePreparation(value, agentID)
    } catch (e) { if (current === generation) error = e.status === 404 ? 'Model preparation is unavailable on this gateway. Update the server to use it.' : e.message }
    finally { if (current === generation) busy = false }
  }
  onMount(() => {
    let first = true
    const unsubscribe = apiKey.subscribe(() => { if (first) { first = false; return }; clear(); error = 'Sign-in changed. Refresh model preparation.' })
    load(); return unsubscribe
  })
  onDestroy(clear)
</script>

<section class="preparation" aria-label="Model preparation" aria-busy={busy}>
  <header><strong>Model preparation</strong><button type="button" on:click={load} disabled={busy}>Refresh preparation</button></header>
  <p>Saved configuration. Save edits before refreshing. Each run prepares its selected model; metadata may be cached for up to five minutes.</p>
  {#if busy}<p role="status">Checking model metadata…</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  {#if result}
    <div class="identity">{#if result.strategy === 'workflow'}Models selected per workflow step{:else if result.strategy === 'router'}No model needed for message routing{:else}{result.profile.provider || 'No provider'} · {result.profile.model || 'Provider chooses at runtime'}{/if}</div>
    <p>{strategyLabel(result.strategy)} · Your goal, permissions and spending limits stay unchanged.</p>
    {#if result.blocked_reason}<p role="alert">{result.blocked_reason}</p>{/if}
    <details><summary>Capabilities and working approach</summary>
      <dl>
        <dt>Native tools</dt><dd>{supportLabel(result.profile.native_tools)}</dd>
        <dt>Reasoning</dt><dd>{supportLabel(result.profile.reasoning)}</dd>
        <dt>Shared context</dt><dd>{result.profile.context_tokens ? result.profile.context_tokens.toLocaleString() + ' tokens' : 'Not reported'}</dd>
        {#if result.profile.input_tokens}<dt>Input ceiling</dt><dd>{result.profile.input_tokens.toLocaleString()} tokens</dd>{/if}
        {#if result.profile.output_tokens}<dt>Output ceiling</dt><dd>{result.profile.output_tokens.toLocaleString()} tokens</dd>{/if}
        <dt>Evidence</dt><dd>{result.profile.source === 'provider_metadata' ? 'Provider metadata, with configured limits where available' : result.profile.source === 'adapter' ? 'Adapter configuration; model capabilities may be unknown' : 'Capabilities not reported'}</dd>
      </dl>
      <ul>{#each result.approach as step}<li>{step}</li>{/each}</ul>
      {#each result.warnings as warning}<p class="warning">{warning}</p>{/each}
      <p>This is capability-aware preparation, not a benchmark or a guarantee of correctness.</p>
    </details>
  {/if}
</section>

<style>
  .preparation { margin: 12px 0; padding: 14px; border: 1px solid var(--border, #394150); border-radius: 12px; font-size: 13px; overflow-wrap: anywhere; }
  header { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
  p { line-height:1.5; color:var(--text-muted, #909cad); margin:8px 0; }
  .identity { margin-top:12px; font-weight:600; }
  summary { cursor:pointer; margin-top:10px; }
  dl { display:grid; grid-template-columns: minmax(80px, 1fr) 2fr; gap:8px; }
  dd { margin:0; } li { margin:8px 0; line-height:1.5; }
  [role=alert], .warning { color:var(--warning, #d3a451); }
  button { font:inherit; cursor:pointer; }
</style>
