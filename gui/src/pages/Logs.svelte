<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { stripAnsi, logLevel, LEVEL_COLORS, LEVEL_BADGES } from '../lib/logutils.js'

  let lines      = []
  let source     = ''
  let note       = ''
  let loading    = true
  let error      = ''
  let filter     = ''
  let lineCount  = 500
  let autoRefresh = false
  let timer      = null
  let logEl
  let levelFilter = 'all'   // all | error | warn | info | debug
  let wrap        = true    // wrap long lines vs. horizontal scroll

  const LEVELS = ['all', 'error', 'warn', 'info', 'debug']

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.logs.get(lineCount, filter)
      lines  = res.lines  || []
      source = res.source || ''
      note   = res.note   || ''
      scrollToBottom()
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function scrollToBottom() {
    setTimeout(() => {
      if (logEl) logEl.scrollTop = logEl.scrollHeight
    }, 50)
  }

  function toggleAutoRefresh() {
    autoRefresh = !autoRefresh
    if (autoRefresh) {
      timer = setInterval(load, 3000)
    } else {
      clearInterval(timer)
      timer = null
    }
  }

  function handleFilterKey(e) {
    if (e.key === 'Enter') load()
  }

  // Pre-process once per load: strip ANSI escapes and classify severity.
  $: processed = lines.map(raw => {
    const text = stripAnsi(raw)
    const level = logLevel(raw)
    return { text, level }
  })
  $: levelCounts = processed.reduce((acc, l) => {
    acc[l.level] = (acc[l.level] || 0) + 1
    return acc
  }, {})
  $: visible = levelFilter === 'all'
    ? processed
    : processed.filter(l => l.level === levelFilter)

  onMount(load)
  onDestroy(() => { if (timer) clearInterval(timer) })
</script>

<div class="page">
  <div class="page-header">
    <h1>Logs</h1>
    <div class="header-actions">
      <button class="btn-secondary" class:active={autoRefresh} on:click={toggleAutoRefresh}>
        {autoRefresh ? '⏹ Stop auto-refresh' : '▶ Auto-refresh (3s)'}
      </button>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
        <TourButton />
    </div>

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if note}
    <div class="banner info">{note}</div>
  {/if}

  <div class="toolbar">
    <div class="filter-wrap">
      <input
        bind:value={filter}
        placeholder="Filter lines… (Enter to apply)"
        class="filter-input"
        on:keydown={handleFilterKey}
      />
      {#if filter}
        <button class="clear-filter" on:click={() => { filter = ''; load() }}>✕</button>
      {/if}
    </div>
    <select bind:value={lineCount} on:change={load} class="lines-select" aria-label="Number of lines to show">
      <option value={100}>Last 100 lines</option>
      <option value={500}>Last 500 lines</option>
      <option value={1000}>Last 1 000 lines</option>
      <option value={5000}>Last 5 000 lines</option>
    </select>
    <button class="btn-secondary small-btn" class:active={!wrap} on:click={() => wrap = !wrap}
            title={wrap ? 'Long lines wrap — click for horizontal scroll' : 'Long lines scroll — click to wrap'}>
      {wrap ? '↩ Wrap' : '↔ Scroll'}
    </button>
    {#if lines.length > 0}
      <button class="btn-secondary small-btn" on:click={() => lines = []}>Clear view</button>
    {/if}
  </div>

  <div class="level-tabs" role="group" aria-label="Filter by log level">
    {#each LEVELS as lv}
      <button class="level-tab" class:lt-active={levelFilter === lv}
              style={levelFilter === lv && lv !== 'all' ? `color:${LEVEL_COLORS[lv]}` : ''}
              on:click={() => levelFilter = lv}>
        {lv === 'all' ? `All (${lines.length})` : `${LEVEL_BADGES[lv]} (${levelCounts[lv] || 0})`}
      </button>
    {/each}
  </div>

  {#if source}
    <div class="source-bar">
      <span class="source-label">Source</span>
      <code class="source-path">{source}</code>
      <span class="source-count">{lines.length} lines</span>
    </div>
  {/if}

  <div class="log-panel" class:nowrap={!wrap} bind:this={logEl}>
    {#if loading && lines.length === 0}
      <div class="empty">Loading logs…</div>
    {:else if visible.length === 0}
      <div class="empty">
        {#if lines.length > 0}
          No {levelFilter} lines in the current view.
        {:else if filter}
          No lines match <strong>"{filter}"</strong>.
        {:else}
          No log lines available.
          {#if !source || source === 'stdout'}
            <br>Set <code>log.file</code> in your config to enable file logging.
          {/if}
        {/if}
      </div>
    {:else}
      {#each visible as line}
        <div class="log-line" style="color:{LEVEL_COLORS[line.level]}">
          <span class="log-badge" style="color:{LEVEL_COLORS[line.level]}">{LEVEL_BADGES[line.level]}</span>
          <span class="log-text">{line.text}</span>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .banner { padding: .65rem 1rem; border-radius: 8px; font-size: .82rem; flex-shrink: 0; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .info { background: rgba(108,99,255,.08); border: 1px solid rgba(108,99,255,.2); color: #8b85ff; }

  .active { background: rgba(108,99,255,.15) !important; color: #8b85ff !important; border-color: rgba(108,99,255,.4) !important; }

  .toolbar {
    display: flex; gap: .5rem; align-items: center; flex-shrink: 0; flex-wrap: wrap;
  }
  .filter-wrap { position: relative; flex: 1; min-width: 180px; }
  .filter-input { width: 100%; padding-right: 2rem; }
  .clear-filter {
    position: absolute; right: .4rem; top: 50%; transform: translateY(-50%);
    background: none; font-size: .75rem; color: #555a7a; padding: 0;
    line-height: 1;
  }
  .clear-filter:hover { color: #e8eaf6; }
  .lines-select { width: 160px; flex-shrink: 0; }
  .small-btn { padding: .4rem .75rem; font-size: .78rem; border-radius: 6px; flex-shrink: 0; }

  .source-bar {
    display: flex; align-items: center; gap: .75rem;
    padding: .5rem .85rem; background: #0e1020; border: 1px solid #1a1e36;
    border-radius: 8px; flex-shrink: 0;
  }
  .source-label { font-size: .7rem; color: #555a7a; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .source-path  { font-size: .78rem; color: #7b82a8; flex: 1; word-break: break-all; }
  .source-count { font-size: .72rem; color: #555a7a; flex-shrink: 0; }

  .level-tabs {
    display: inline-flex; gap: .25rem; padding: .15rem; flex-shrink: 0; align-self: flex-start;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    flex-wrap: wrap;
  }
  .level-tab {
    background: transparent; color: #7b82a8; border: 0; border-radius: 6px;
    padding: .25rem .6rem; font-size: .72rem; font-family: monospace; cursor: pointer;
  }
  .level-tab:hover { color: #f0f2ff; }
  .level-tab.lt-active { background: #262b4c; color: #f0f2ff; }

  .log-panel {
    flex: 1; min-height: 0; overflow-y: auto;
    background: #0a0c17; border: 1px solid #1a1e36; border-radius: 10px;
    font-family: 'JetBrains Mono', 'Fira Code', monospace; font-size: .76rem;
    line-height: 1.55;
  }
  .log-panel.nowrap { overflow-x: auto; }
  .log-panel.nowrap .log-line { width: max-content; min-width: 100%; }
  .log-panel.nowrap .log-text { white-space: pre; }
  .empty {
    padding: 3rem 2rem; text-align: center; color: #555a7a; font-family: inherit;
  }
  .empty code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; }

  .log-line {
    display: flex; align-items: flex-start; gap: .6rem;
    padding: .22rem .85rem; border-bottom: 1px solid rgba(255,255,255,.025);
    word-break: break-all;
  }
  .log-line:hover { background: rgba(255,255,255,.03); }
  .log-badge { flex-shrink: 0; font-weight: 700; font-size: .68rem; width: 2.6rem; }
  .log-text  { flex: 1; white-space: pre-wrap; }
</style>
