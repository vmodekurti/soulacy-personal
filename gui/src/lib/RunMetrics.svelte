<script>
  // Compact per-run metrics strip (Story 7). Fetches once per sessionId
  // (+refreshKey) and renders nothing when no metrics exist — safe to drop
  // into any list row.
  import { api } from './api.js'
  import { metricParts } from './metrics.js'

  export let sessionId = ''
  export let agentId = ''
  // Bump to force a re-fetch (e.g. after a new chat reply lands).
  export let refreshKey = 0

  let metrics = null
  let failure = ''

  async function load(sid, aid, _key) {
    if (!sid) { metrics = null; failure = ''; return }
    try {
      metrics = await api.runs.metrics(sid, aid)
      failure = metrics?.failure || ''
    } catch {
      metrics = null
      failure = ''
    }
  }

  $: load(sessionId, agentId, refreshKey)
  $: parts = metricParts(metrics)
</script>

{#if parts.length > 0 || failure}
  <span class="run-metrics" title="session {sessionId}">
    {#each parts as p, i}
      {#if i > 0}<span class="sep">·</span>{/if}
      <span class="part">{p}</span>
    {/each}
    {#if failure}
      <span class="sep">·</span>
      <span class="fail" title={failure}>⚠ {failure.length > 60 ? failure.slice(0, 60) + '…' : failure}</span>
    {/if}
  </span>
{/if}

<style>
  .run-metrics {
    display: inline-flex; flex-wrap: wrap; align-items: center; gap: 0.35rem;
    font-size: 0.7rem; font-family: monospace; color: #6b7294;
    line-height: 1.4;
  }
  .sep { color: #3d4360; }
  .part { white-space: nowrap; }
  .fail { color: #ff9a9a; overflow-wrap: anywhere; }
</style>
