<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { apiKey } from '../lib/stores.js'
  import TourButton from '../lib/TourButton.svelte'
  import { reportWindows, validateReport, count, spend, reportAttention, downloadReport } from '../lib/operationsReport.js'

  let window = '24h', report = null, error = '', loading = false, generation = 0
  $: activity = report?.activity
  $: usage = report?.usage
  $: attention = report ? reportAttention(report) : []
  async function refresh() {
    const current = ++generation, requestedWindow = window
    loading = true; report = null; error = ''
    try {
      const result = await api.operationsReport(requestedWindow)
      if (current === generation) report = validateReport(result, requestedWindow)
    } catch (e) {
      if (current === generation) error = e.status === 403 ? 'Administrator access with metrics:read permission is required to view operations reports.'
        : e.status === 404 ? 'This gateway does not support operations reports yet. Update the gateway to use this page.'
        : e.message || 'Could not load the report. Retry when the gateway is available.'
    } finally { if (current === generation) loading = false }
  }
  onMount(() => {
    let first = true
    const unsubscribe = apiKey.subscribe(() => {
      if (first) { first = false; return }
      generation++; report = null; loading = false
      error = 'Gateway sign-in changed. Refresh to load a new report.'
    })
    refresh()
    return unsubscribe
  })
  onDestroy(() => { generation++; report = null })
</script>

<section class="reports" aria-label="Operations reports" aria-busy={loading}>
  <header>
    <div><h1>Operations reports</h1><p>What Soulacy did, what it cost, and what needs a closer look.</p></div>
    <div class="links"><TourButton page="reports" /><a href="https://docs.soulacy.io/using/operations-reports/" target="_blank" rel="noopener noreferrer">Report guide ↗</a></div>
  </header>
  <div class="controls">
    <label>Reporting period <select bind:value={window} on:change={refresh}>{#each reportWindows as option}<option value={option.value}>{option.label}</option>{/each}</select></label>
    <button class="btn-primary" on:click={refresh} disabled={loading}>Refresh report</button>
    <div class="exports" aria-label="Download report">
      <button class="btn-secondary" disabled={!report || loading || report.status === 'unavailable'} on:click={() => downloadReport(report, 'md')}>Markdown</button>
      <button class="btn-secondary" disabled={!report || loading || report.status === 'unavailable'} on:click={() => downloadReport(report, 'csv')}>CSV</button>
      <button class="btn-secondary" disabled={!report || loading || report.status === 'unavailable'} on:click={() => downloadReport(report, 'json')}>JSON</button>
    </div>
  </div>
  <p class="privacy">Read-only · no AI calls · no automatic sending · chat content excluded</p>
  {#if loading}<p role="status">Building your report from recorded activity and usage…</p>{/if}
  {#if error}<p class="notice" role="alert">{error}</p>{/if}
  {#if report}
    <p class="period">{report.start} → {report.end} · UTC, end exclusive · {report.status === 'ready' ? 'Both sources available' : report.status === 'partial' ? 'Partial data' : 'Data unavailable'}</p>
    <div class="cards">
      <article><h2>Incoming requests</h2><strong>{count(activity?.totals.requests)}</strong><p>Sessions: {count(activity?.totals.sessions)}. One session can contain several requests.</p></article>
      <article><h2>Reply-complete sessions</h2><strong>{count(activity?.totals.reply_complete_sessions)}</strong><p>Replies cover requests; no recorded error. Not a quality or delivery guarantee.</p></article>
      <article><h2>Sessions with errors</h2><strong>{count(activity?.totals.error_sessions)}</strong><p>Other sessions without enough replies: {count(activity?.totals.unresolved_sessions)}.</p></article>
      <article><h2>Recorded model cost</h2><strong>{spend(usage?.totals.cost_micros)}</strong><p>Estimated USD · Requests with unknown pricing: {count(usage?.totals.unknown_priced)}.</p></article>
    </div>
    <section class="panel" aria-label="Check next"><h2>Check next</h2>
      {#if attention.length}<ul>{#each attention as item}<li>{item}</li>{/each}</ul>
      {:else}<p>No flagged conditions in the available records. Check the source coverage before treating this as a clean bill of health.</p>{/if}
      <div class="links"><a href="#activity">Inspect Runs →</a><a href="#providers">Providers →</a><a href="#schedule">Automations →</a></div>
    </section>
    <section class="panel" aria-label="Activity by agent"><h2>Activity by agent</h2>
      {#if !activity}<p>{report.sources.activity.detail}</p>
      {:else if !activity.by_agent.length}<p>No retained activity records in this period. This does not prove the gateway was running.</p>
      {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard focus enables horizontal scrolling of the report table.) -->
      <div class="table-scroll" tabindex="0" role="region" aria-label="Agent activity table"><table>
        <thead><tr><th>Agent</th><th>Requests</th><th>Sessions</th><th>Reply-complete</th><th>With errors</th><th>Unresolved</th><th>Tool calls</th></tr></thead>
        <tbody>{#each activity.by_agent as row}<tr><th scope="row">{row.agent_id || '(unattributed)'}</th><td>{count(row.requests)}</td><td>{count(row.sessions)}</td><td>{count(row.reply_complete_sessions)}</td><td>{count(row.error_sessions)}</td><td>{count(row.unresolved_sessions)}</td><td>{count(row.tool_calls)}</td></tr>{/each}</tbody>
      </table></div>{/if}
      {#if activity?.single_request_reply_latency}
        <p>Single-request reply latency: average {count(activity.single_request_reply_latency.avg_ms)} ms · p95 {count(activity.single_request_reply_latency.p95_ms)} ms · Samples: {count(activity.single_request_reply_latency.samples)}. Multi-request sessions excluded.</p>
      {:else}<p>Single-request reply latency: no qualifying samples available.</p>{/if}
    </section>
    <section class="panel" aria-label="Model usage"><h2>Model usage</h2>
      {#if usage}<p>{count(usage.totals.requests)} model requests (including rejected) · {count(usage.totals.attempts)} provider attempts · {count(usage.totals.total_tokens)} tokens · {count(usage.totals.failed)} failed requests · {count(usage.totals.rejected)} rejected before execution.</p>
        {#each ['by_agent', 'by_model'] as grouping}
          <h3>{grouping === 'by_agent' ? 'Cost by agent' : 'Cost by model'}</h3>
          {#if !usage[grouping].length}<p>No recorded model usage in this period.</p>
          {:else}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard focus enables horizontal scrolling of the report table.) -->
          <div class="table-scroll" tabindex="0" role="region" aria-label={grouping === 'by_agent' ? 'Agent cost table' : 'Model cost table'}><table>
            <thead><tr><th>{grouping === 'by_agent' ? 'Agent' : 'Provider / model'}</th><th>Requests</th><th>Attempts</th><th>Tokens</th><th>Estimated USD</th><th>Unpriced</th></tr></thead>
            <tbody>{#each usage[grouping] as row}<tr><th scope="row">{grouping === 'by_agent' ? row.agent_id || '(unattributed)' : `${row.provider || '(unknown)'} / ${row.model || '(unknown)'}`}</th><td>{count(row.requests)}</td><td>{count(row.attempts)}</td><td>{count(row.total_tokens)}</td><td>{spend(row.cost_micros)}</td><td>{count(row.unknown_priced)}</td></tr>{/each}</tbody>
          </table></div>{/if}
        {/each}
      {:else}<p>{report.sources.usage.detail}</p>{/if}
    </section>
    <section class="panel" aria-label="Sources and limitations"><h2>Sources and limitations</h2>
      {#each Object.entries(report.sources) as [key, source]}<p><strong>{key === 'activity' ? 'Activity' : 'Usage'} — {source.status}:</strong> {source.detail}</p>{/each}
      <ul>{#each report.notes as note}<li>{note}</li>{/each}</ul>
      <p>Downloads contain this displayed snapshot and may include private agent/model names. No report is stored in browser storage or sent automatically.</p>
    </section>
  {/if}
</section>

<style>
  .reports { padding:28px; width:100%; box-sizing:border-box; max-width:1440px; margin:0 auto; min-width:0; }
  header,.controls,.exports,.links { display:flex; gap:12px; align-items:center; flex-wrap:wrap; }
  header { justify-content:space-between; margin-bottom:24px; } h1 { font-size:1.65rem; margin:0 0 8px; }
  h2 { font-size:1rem; margin:0 0 12px; } h3 { font-size:.9rem; margin:22px 0 10px; }
  p,li { color:var(--text-secondary,#a7adc2); font-size:.85rem; line-height:1.65; } p { margin:6px 0; }
  a { color:var(--accent,#9294ff); font-size:.85rem; } label { display:flex; gap:10px; align-items:center; font-size:.85rem; }
  select { background:var(--bg-secondary,#151728); color:var(--text,#e9edf8); border:1px solid var(--border,#34374b); border-radius:8px; padding:10px; }
  .privacy { margin:12px 0 20px; } .period { overflow-wrap:anywhere; margin-bottom:14px; }
  .cards { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:14px; }
  .cards article,.panel { background:var(--bg-secondary,#121523); border:1px solid var(--border,#30354c); padding:20px; border-radius:12px; min-width:0; }
  .cards h2 { color:var(--text-secondary,#a7adc2); font-size:.8rem; font-weight:500; }
  .cards strong { display:block; font-size:1.8rem; overflow-wrap:anywhere; } .panel { margin-top:18px; }
  .table-scroll { overflow-x:auto; max-width:100%; } table { width:100%; border-collapse:collapse; font-size:.8rem; text-align:right; }
  th,td { padding:12px 10px; border-bottom:1px solid var(--border,#30354c); white-space:nowrap; }
  th:first-child { text-align:left; white-space:normal; overflow-wrap:anywhere; min-width:130px; max-width:260px; }
  thead th { color:var(--text-secondary,#a7adc2); font-weight:500; } tbody th { font-weight:500; }
  ul { padding-left:20px; } li { margin-bottom:8px; } .notice { padding:16px; border:1px solid #a16f35; border-radius:10px; }
  button:disabled { opacity:.5; cursor:not-allowed; }
  @media(max-width:1100px) { .cards { grid-template-columns:repeat(2,minmax(0,1fr)); } }
  @media(max-width:600px) { .reports { padding:16px; } .cards { grid-template-columns:1fr; } .cards article,.panel { padding:16px; } header { margin-bottom:18px; } }
</style>
