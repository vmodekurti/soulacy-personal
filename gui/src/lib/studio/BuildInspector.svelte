<script>
  /*
   * BuildInspector — the durable build's transcript, made visible.
   *
   * The autonomous build-verify-repair loop records a structured, timestamped
   * event for every phase it runs (draft snapshot → preflight → each repair →
   * each verify → result), persisted via the /studio/build-trace API. This panel
   * renders that trace as an ordered timeline so a build — including one that
   * failed, or a 6am scheduled one — is debuggable WITHOUT reading server logs:
   * what ran, in what order, how long it took, what was wrong, and what the loop
   * did about it. ("The transcript is the UI.")
   *
   * Zero dependencies: themes off the app's CSS variables, no editor/chart libs.
   */
  import { createEventDispatcher, onDestroy } from 'svelte'
  import { bridge } from './studioApi.js'
  import { buildReplayFrames } from './replay.js'

  // open toggles the overlay; traceId selects a build (null/'' = most recent).
  export let open = false
  export let traceId = null

  const dispatch = createEventDispatcher()

  let loading = false
  let error = ''
  let trace = null // { id, intent, start, events[] }
  let recent = [] // [{ id, intent, start, events, last }]
  let diskDir = ''
  let expanded = new Set()

  // ── Replay scrubber ─────────────────────────────────────────────────────────
  // Step the build attempt-by-attempt; each frame drives the canvas node accents
  // (red → amber → green) via a `frame` event the parent applies as an override.
  let frames = []
  let frameIdx = -1
  let playTimer = null

  function emitFrame() {
    const f = frameIdx >= 0 && frameIdx < frames.length ? frames[frameIdx] : null
    dispatch('frame', f ? f.byId : null)
  }
  function gotoFrame(i) {
    if (!frames.length) return
    frameIdx = Math.max(0, Math.min(frames.length - 1, i))
    emitFrame()
  }
  function stepFrame(d) {
    stopPlay()
    gotoFrame(frameIdx + d)
  }
  function stopPlay() {
    if (playTimer) {
      clearInterval(playTimer)
      playTimer = null
    }
  }
  function togglePlay() {
    if (playTimer) {
      stopPlay()
      return
    }
    if (frameIdx >= frames.length - 1) gotoFrame(0)
    playTimer = setInterval(() => {
      if (frameIdx >= frames.length - 1) {
        stopPlay()
        return
      }
      gotoFrame(frameIdx + 1)
    }, 1100)
  }
  onDestroy(stopPlay)

  // Reload whenever the panel opens or the selected build changes.
  let lastKey = ''
  $: if (open) {
    const key = String(traceId || '')
    if (key !== lastKey) {
      lastKey = key
      load(key)
    }
  } else {
    lastKey = ''
  }

  async function load(id) {
    loading = true
    error = ''
    expanded = new Set()
    try {
      const [t, list] = await Promise.all([
        bridge.buildTrace(id || undefined),
        bridge.buildTraces().catch(() => ({ traces: [], dir: '' })),
      ])
      trace = t
      recent = (list && list.traces) || []
      diskDir = (list && list.dir) || ''
      // Build the replay timeline for this trace and reset the scrubber.
      stopPlay()
      frames = buildReplayFrames(trace)
      frameIdx = frames.length ? frames.length - 1 : -1
      emitFrame()
    } catch (e) {
      error = (e && e.message) || 'could not load build trace'
      trace = null
    } finally {
      loading = false
    }
  }

  function selectBuild(id) {
    traceId = id // reactive block reloads
  }

  function close() {
    stopPlay()
    dispatch('frame', null) // clear the canvas replay override
    dispatch('close')
  }

  function toggle(seq) {
    const next = new Set(expanded)
    next.has(seq) ? next.delete(seq) : next.add(seq)
    expanded = next
  }

  // ── formatting helpers ──────────────────────────────────────────────────────
  function relTime(ms) {
    if (ms == null) return ''
    if (ms < 1000) return '+' + ms + 'ms'
    return '+' + (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + 's'
  }
  function dur(ms) {
    if (!ms) return ''
    if (ms < 1000) return ms + 'ms'
    return (ms / 1000).toFixed(1) + 's'
  }
  function clock(iso) {
    try {
      return new Date(iso).toLocaleTimeString()
    } catch (_) {
      return ''
    }
  }
  // Kind → a stable accent class for the dot + badge.
  function kindClass(kind) {
    switch (kind) {
      case 'error':
        return 'k-error'
      case 'verify':
        return 'k-verify'
      case 'repair':
        return 'k-repair'
      case 'result':
        return 'k-result'
      case 'preflight':
        return 'k-preflight'
      case 'snapshot':
        return 'k-snapshot'
      default:
        return 'k-phase'
    }
  }
  // A short, friendly one-liner derived from an event's structured data, shown
  // inline so the common case needs no expand.
  function dataHint(e) {
    const d = e.data
    if (!d) return ''
    if (e.kind === 'snapshot') {
      const parts = []
      if (d.nodes != null) parts.push(d.nodes + (d.nodes === 1 ? ' node' : ' nodes'))
      if (d.hash) parts.push('#' + d.hash)
      return parts.join(' · ')
    }
    if (e.kind === 'preflight') {
      const probs = Array.isArray(d.problems) ? d.problems.length : 0
      return probs ? probs + (probs === 1 ? ' problem' : ' problems') : 'clean'
    }
    if (e.kind === 'verify') {
      if (d.ok === true) return d.real ? 'real run passed' : 'dry run passed'
      if (d.ok === false) return 'run failed'
    }
    if (e.kind === 'repair' && d.changed != null) {
      return d.changed ? 'changed the draft' : 'no change'
    }
    if (e.kind === 'result') {
      return d.verified ? 'verified' : d.ok ? 'validated' : 'unresolved'
    }
    return ''
  }
  // Whether an event has detail worth expanding.
  function hasDetail(e) {
    return !!(e.data && Object.keys(e.data).length) || !!e.error
  }
  function pretty(obj) {
    try {
      return JSON.stringify(obj, null, 2)
    } catch (_) {
      return String(obj)
    }
  }
</script>

{#if open}
  <div class="bi-backdrop" on:click={close} role="presentation"></div>
  <aside class="bi-panel" aria-label="Build inspector">
    <header class="bi-head">
      <div class="bi-title">
        <span class="bi-dot"></span>
        <div>
          <div class="bi-h">Build inspector</div>
          <div class="bi-sub">
            {#if trace && trace.intent}{trace.intent}{:else}every step the build took{/if}
          </div>
        </div>
      </div>
      <button class="bi-x" title="Close" on:click={close}>✕</button>
    </header>

    {#if recent.length > 1}
      <div class="bi-picker">
        <label for="bi-recent">Build</label>
        <select id="bi-recent" on:change={(e) => selectBuild(e.target.value)} value={trace ? trace.id : ''}>
          {#each recent as r}
            <option value={r.id}>
              {clock(r.start)} — {r.intent || r.id} ({r.events} events)
            </option>
          {/each}
        </select>
      </div>
    {/if}

    {#if frames.length > 1}
      <div class="bi-replay">
        <button class="bi-rbtn" title="Step back" on:click={() => stepFrame(-1)} disabled={frameIdx <= 0}>◀</button>
        <button class="bi-rbtn play" title={playTimer ? 'Pause' : 'Replay the build'} on:click={togglePlay}>
          {playTimer ? '⏸' : '▶'}
        </button>
        <button class="bi-rbtn" title="Step forward" on:click={() => stepFrame(1)} disabled={frameIdx >= frames.length - 1}>▶</button>
        <input
          class="bi-scrub"
          type="range"
          min="0"
          max={frames.length - 1}
          value={frameIdx}
          on:input={(e) => { stopPlay(); gotoFrame(+e.target.value) }}
        />
        <span class="bi-frame-label">{frameIdx >= 0 ? frames[frameIdx].label : ''}</span>
      </div>
    {/if}

    <div class="bi-body">
      {#if loading}
        <div class="bi-empty">Loading trace…</div>
      {:else if error}
        <div class="bi-empty bi-err">{error}</div>
      {:else if !trace || !trace.events || !trace.events.length}
        <div class="bi-empty">No build trace yet. Run “Build until it works” to record one.</div>
      {:else}
        <ol class="bi-timeline">
          {#each trace.events as e (e.seq)}
            <li class="bi-event {kindClass(e.kind)}" class:has-error={!!e.error}>
              <div class="bi-row" on:click={() => hasDetail(e) && toggle(e.seq)} class:click={hasDetail(e)} role="button" tabindex="0"
                   on:keydown={(ev) => ev.key === 'Enter' && hasDetail(e) && toggle(e.seq)}>
                <span class="bi-rel" title={clock(e.at)}>{relTime(e.elapsed_ms)}</span>
                <span class="bi-badge">{e.kind}</span>
                {#if e.attempt}<span class="bi-attempt">#{e.attempt}</span>{/if}
                <span class="bi-msg">{e.message}</span>
                {#if dataHint(e)}<span class="bi-hint">{dataHint(e)}</span>{/if}
                {#if e.dur_ms}<span class="bi-dur">{dur(e.dur_ms)}</span>{/if}
                {#if hasDetail(e)}<span class="bi-caret">{expanded.has(e.seq) ? '▾' : '▸'}</span>{/if}
              </div>
              {#if e.error}
                <div class="bi-error">{e.error}</div>
              {/if}
              {#if expanded.has(e.seq) && e.data}
                {#if Array.isArray(e.data.problems) && e.data.problems.length}
                  <ul class="bi-problems">
                    {#each e.data.problems as p}<li>{p}</li>{/each}
                  </ul>
                {/if}
                <pre class="bi-json">{pretty(e.data)}</pre>
              {/if}
            </li>
          {/each}
        </ol>
      {/if}
    </div>

    <footer class="bi-foot">
      {#if diskDir}
        <span title="Each build is also written here as a JSONL log">logs: <code>{diskDir}</code></span>
      {:else}
        <span>in-memory traces · set <code>SOULACY_STUDIO_TRACE_DIR</code> to persist</span>
      {/if}
    </footer>
  </aside>
{/if}

<style>
  .bi-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    z-index: 60;
  }
  .bi-panel {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    width: min(560px, 94vw);
    background: var(--bg-elev, #fff);
    border-left: 1px solid var(--border, #e3e3e3);
    box-shadow: -8px 0 28px rgba(0, 0, 0, 0.18);
    z-index: 61;
    display: flex;
    flex-direction: column;
    animation: bi-in 160ms ease-out;
  }
  @keyframes bi-in {
    from { transform: translateX(12px); opacity: 0; }
    to { transform: translateX(0); opacity: 1; }
  }
  .bi-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border, #e3e3e3);
  }
  .bi-title { display: flex; gap: 10px; align-items: center; min-width: 0; }
  .bi-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent, #4f7cff); flex: none; }
  .bi-h { font-weight: 600; font-size: 14px; color: var(--text, #1a1a1a); }
  .bi-sub {
    font-size: 12px; color: var(--text-muted, #777);
    max-width: 420px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .bi-x {
    border: none; background: transparent; cursor: pointer;
    color: var(--text-muted, #777); font-size: 14px; padding: 4px 8px; border-radius: 6px;
  }
  .bi-x:hover { background: var(--bg-elev-2, #f2f2f2); color: var(--text, #1a1a1a); }
  .bi-picker {
    display: flex; align-items: center; gap: 8px;
    padding: 10px 16px; border-bottom: 1px solid var(--border, #e3e3e3);
    font-size: 12px; color: var(--text-muted, #777);
  }
  .bi-picker select {
    flex: 1; padding: 5px 8px; border-radius: 6px;
    border: 1px solid var(--border, #e3e3e3);
    background: var(--bg, #fff); color: var(--text, #1a1a1a); font-size: 12px;
  }
  .bi-replay {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border, #e3e3e3);
  }
  .bi-rbtn {
    border: 1px solid var(--border, #e3e3e3);
    background: var(--bg, #fff);
    color: var(--text, #1a1a1a);
    border-radius: 6px;
    width: 26px;
    height: 26px;
    cursor: pointer;
    font-size: 11px;
    flex: none;
  }
  .bi-rbtn.play { background: var(--accent, #4f7cff); color: #fff; border-color: transparent; }
  .bi-rbtn:disabled { opacity: 0.4; cursor: default; }
  .bi-scrub { flex: 1; accent-color: var(--accent, #4f7cff); }
  .bi-frame-label {
    flex: none;
    min-width: 72px;
    font-size: 11px;
    color: var(--text-muted, #777);
    text-align: right;
  }

  .bi-body { flex: 1; overflow-y: auto; padding: 8px 0; }
  .bi-empty { padding: 32px 16px; text-align: center; color: var(--text-muted, #777); font-size: 13px; }
  .bi-err { color: #c0392b; }

  .bi-timeline { list-style: none; margin: 0; padding: 0; }
  .bi-event {
    position: relative;
    padding: 2px 16px 2px 26px;
    border-left: 2px solid transparent;
  }
  /* timeline spine + dot */
  .bi-event::before {
    content: '';
    position: absolute; left: 14px; top: 12px;
    width: 7px; height: 7px; border-radius: 50%;
    background: var(--accent, #4f7cff);
  }
  .bi-event::after {
    content: '';
    position: absolute; left: 17px; top: 0; bottom: 0;
    width: 1px; background: var(--border, #e7e7e7);
  }
  .bi-event:last-child::after { bottom: 50%; }
  .k-error::before { background: #c0392b; }
  .k-verify::before { background: #2e9e5b; }
  .k-repair::before { background: #d98a1f; }
  .k-result::before { background: var(--accent, #4f7cff); }
  .k-preflight::before { background: #6c7a89; }
  .k-snapshot::before { background: #9b59b6; }

  .bi-row {
    display: flex; align-items: baseline; gap: 8px;
    padding: 6px 0; font-size: 12.5px; color: var(--text, #1a1a1a);
  }
  .bi-row.click { cursor: pointer; }
  .bi-row.click:hover { background: var(--bg-elev-2, #f6f6f6); border-radius: 6px; }
  .bi-rel {
    flex: none; width: 52px; text-align: right;
    color: var(--text-muted, #999); font-variant-numeric: tabular-nums;
    font-size: 11px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .bi-badge {
    flex: none; font-size: 10px; text-transform: uppercase; letter-spacing: 0.04em;
    color: var(--text-muted, #777);
    border: 1px solid var(--border, #e3e3e3); border-radius: 4px;
    padding: 1px 5px;
  }
  .bi-attempt { flex: none; font-size: 11px; color: var(--text-muted, #999); }
  .bi-msg { flex: 1; min-width: 0; }
  .bi-hint {
    flex: none; font-size: 11px; color: var(--text-muted, #888);
    background: var(--bg-elev-2, #f2f2f2); border-radius: 4px; padding: 1px 6px;
  }
  .bi-dur {
    flex: none; font-size: 11px; color: var(--text-muted, #999);
    font-variant-numeric: tabular-nums;
  }
  .bi-caret { flex: none; color: var(--text-muted, #aaa); font-size: 10px; }
  .has-error .bi-msg { color: #c0392b; }
  .bi-error {
    margin: 2px 0 6px; padding: 6px 8px;
    background: rgba(192, 57, 43, 0.08); border-radius: 6px;
    color: #c0392b; font-size: 12px; white-space: pre-wrap;
  }
  .bi-problems { margin: 4px 0; padding-left: 18px; font-size: 12px; color: var(--text, #333); }
  .bi-json {
    margin: 4px 0 8px; padding: 8px 10px; max-height: 240px; overflow: auto;
    background: var(--bg, #fafafa); border: 1px solid var(--border, #ececec);
    border-radius: 6px; font-size: 11px; line-height: 1.5;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    color: var(--text, #333); white-space: pre;
  }
  .bi-foot {
    padding: 8px 16px; border-top: 1px solid var(--border, #e3e3e3);
    font-size: 11px; color: var(--text-muted, #999);
  }
  .bi-foot code { font-size: 10.5px; }
</style>
