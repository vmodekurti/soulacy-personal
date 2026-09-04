<script>
  import { fallbackLabel } from './fixactions.js'
  // ReadinessPanel — the Save step's "ready to save?" review (ST-07 / ST-16).
  //
  // One verdict assembled server-side, rather than the old client-side stitch of
  // /studio/compile + /studio/security_review + /studio/plan. That stitch had a
  // quiet failure: if the security call errored the GUI dropped the section and
  // still computed `ok` from the two that succeeded, so a draft could look ready
  // on the strength of a review that never ran.
  //
  // So this panel renders FOUR states per section, not two. "Unknown" is shown
  // as prominently as a blocker, because a check that did not run is not a check
  // that passed.

  export let report = null      // /studio/readiness payload
  export let loading = false
  export let error = ''
  export let busy = false
  export let destinations = []       // configured outbound channels [{id,name}]

  export let onRecheck = () => {}
  export let onAction = () => {}   // (item) => void — deep-link to the fix
  export let onReveal = () => {}   // (nodeId) => void
  export let onDestination = () => {} // (channelId, item) => void

  $: sections = (report && report.sections) || []
  $: blockers = (report && report.blockers) || []
  $: warnings = (report && report.warnings) || []
  $: passes = (report && report.ready) || []
  $: unknown = (report && report.unknown) || []

  const STATUS_LABEL = {
    ready: 'Ready', warn: 'Warning', block: 'Blocker', unknown: 'Not checked',
  }
  function statusOf(sec) {
    const s = String((sec && sec.status) || '').toLowerCase()
    if (s === 'ready' || s === 'pass' || s === 'ok') return 'ready'
    if (s === 'warn' || s === 'warning') return 'warn'
    if (s === 'block' || s === 'blocked' || s === 'blocker') return 'block'
    return 'unknown'
  }

  // An item is actionable when the server gave a machine-readable action rather
  // than only prose. Prose tells you what is wrong; an action gets you there.
  function actionable(item) { return !!(item && item.action) }
  // The button text comes from the server, resolved from the shared vocabulary
  // in internal/studio/fixactions.go and attached to the item. This panel used
  // to keep its own switch, which is how the same action came to read
  // "Configure provider" here and "Fix this" in the security panel — and how an
  // earlier version ended up labelling five actions the server never emitted.
  // One list, authored next to the finding, rendered everywhere.
  function actionLabel(item) {
    return (item && item.actionLabel) || fallbackLabel(item && item.action) || 'Fix this'
  }
  function fire(item) {
    if (item && item.action === 'reveal_node' && item.nodeId) onReveal(item.nodeId)
    else onAction(item)
  }
  // `open_delivery` is also used for an already-selected channel whose token
  // needs configuration. Only turn the missing-output case into a picker; the
  // former still belongs on the Delivery settings page.
  function needsDestination(item) {
    if (!item || item.action !== 'open_delivery') return false
    const params = item.actionParams || item.action_params || {}
    if (params.channel) return false
    const text = `${item.message || ''} ${item.fix || ''}`
    return /no\s+(routable\s+)?(outbound\s+)?(output\s+)?(channel|destination)|pick\s+(another\s+)?(one|destination)|choose\s+(a\s+)?destination/i.test(text)
  }
  function chooseDestination(item, event) {
    const id = event.currentTarget.value
    if (!id) return
    onDestination(id, item)
    event.currentTarget.value = ''
  }
</script>

<div class="rp">
  <div class="rp-head">
    <h3>Ready to save?</h3>
    <button class="btn btn-sm" type="button" disabled={loading || busy} on:click={onRecheck}>
      {loading ? 'Checking…' : 'Re-check'}
    </button>
  </div>

  {#if error}
    <div class="rp-error" role="alert">{error}</div>
  {:else if loading && !report}
    <p class="rp-muted">Checking readiness…</p>
  {:else if !report}
    <p class="rp-muted">Nothing checked yet.</p>
  {:else}
    <!-- Headline. Deliberately does NOT say "ready" while any section is
         unknown, even if every section that ran was clean. -->
    <div class="rp-verdict" class:ok={report.ok} class:bad={!report.ok}>
      <strong>
        {#if report.ok}Ready to save
        {:else if blockers.length}{blockers.length} blocker{blockers.length === 1 ? '' : 's'} to resolve
        {:else if unknown.length}Cannot confirm — {unknown.length} check{unknown.length === 1 ? '' : 's'} did not run
        {:else}Not ready{/if}
      </strong>
      {#if report.summary}<span>{report.summary}</span>{/if}
    </div>

    <div class="rp-counts">
      <span class="rp-count block">{blockers.length} blocker{blockers.length === 1 ? '' : 's'}</span>
      <span class="rp-count warn">{warnings.length} warning{warnings.length === 1 ? '' : 's'}</span>
      <span class="rp-count ready">{passes.length} passed</span>
      {#if unknown.length}<span class="rp-count unknown">{unknown.length} not checked</span>{/if}
    </div>

    <ul class="rp-sections">
      {#each sections as sec (sec.id)}
        {@const st = statusOf(sec)}
        <li class="rp-section">
          <div class="rp-sechead">
            <span class="rp-dot {st}" aria-hidden="true"></span>
            <span class="rp-sectitle">{sec.title || sec.id}</span>
            <span class="rp-status {st}">{STATUS_LABEL[st]}</span>
          </div>
          {#if st === 'unknown' && sec.reason}
            <!-- Say WHY it could not be checked. "Not checked" without a reason
                 is indistinguishable from a bug. -->
            <p class="rp-reason">{sec.reason}</p>
          {/if}

          {#each blockers.filter((i) => i.section === sec.id) as item}
            <div class="rp-item block">
              <span class="rp-msg">{item.message || item.kind}</span>
              {#if item.fix}<span class="rp-fix">{item.fix}</span>{/if}
              {#if needsDestination(item) && destinations.length}
                <label class="rp-destination">
                  <span>Deliver results to</span>
                  <select disabled={busy} on:change={(event) => chooseDestination(item, event)}>
                    <option value="">Choose a configured destination…</option>
                    {#each destinations as destination (destination.id)}
                      <option value={destination.id}>{destination.name || destination.id}</option>
                    {/each}
                  </select>
                </label>
              {:else if actionable(item)}
                <button class="btn btn-sm" type="button" disabled={busy} on:click={() => fire(item)}>{actionLabel(item)}</button>
              {:else}
                <!-- No machine action, so say plainly that this one is on the
                     user and where to do it. An item with neither a button nor a
                     next step is a dead end. -->
                <span class="rp-manual">Fix this on the canvas — automatic repair cannot decide it.</span>
                {#if item.nodeId}
                  <button class="btn btn-sm" type="button" disabled={busy} on:click={() => onReveal(item.nodeId)}>Show the step</button>
                {/if}
              {/if}
            </div>
          {/each}

          {#each warnings.filter((i) => i.section === sec.id) as item}
            <div class="rp-item warn">
              <span class="rp-msg">{item.message || item.kind}</span>
              {#if item.fix}<span class="rp-fix">{item.fix}</span>{/if}
              {#if actionable(item)}
                <button class="btn btn-sm" type="button" disabled={busy} on:click={() => fire(item)}>{actionLabel(item)}</button>
              {/if}
            </div>
          {/each}
        </li>
      {/each}
    </ul>

    {#if passes.length}
      <details class="rp-passes">
        <summary>{passes.length} check{passes.length === 1 ? '' : 's'} passed</summary>
        <ul>
          {#each passes as p}<li>{p.message || p.kind}</li>{/each}
        </ul>
      </details>
    {/if}
  {/if}
</div>

<style>
  .rp { display: flex; flex-direction: column; gap: 10px; }
  .rp-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  .rp-head h3 { margin: 0; font-size: .95rem; }
  .rp-muted { font-size: .84rem; color: var(--text-dim, #6b7294); }
  .rp-error {
    padding: 8px 10px; border-radius: 6px; font-size: .82rem;
    color: var(--danger, #e5484d);
    background: color-mix(in srgb, var(--danger, #e5484d) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--danger, #e5484d) 32%, transparent);
  }

  .rp-verdict {
    display: flex; flex-direction: column; gap: 2px;
    padding: 10px 12px; border-radius: 8px; font-size: .85rem;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .rp-verdict.ok {
    background: color-mix(in srgb, var(--ok, #2ea043) 12%, transparent);
    border-color: color-mix(in srgb, var(--ok, #2ea043) 35%, transparent);
  }
  .rp-verdict.bad {
    background: color-mix(in srgb, var(--warn, #f0ad4e) 10%, transparent);
    border-color: color-mix(in srgb, var(--warn, #f0ad4e) 32%, transparent);
  }

  .rp-counts { display: flex; flex-wrap: wrap; gap: 6px; }
  .rp-count { padding: 2px 8px; border-radius: 999px; font-size: .72rem; }
  .rp-count.block   { background: color-mix(in srgb, var(--danger, #e5484d) 16%, transparent); }
  .rp-count.warn    { background: color-mix(in srgb, var(--warn, #f0ad4e) 20%, transparent); }
  .rp-count.ready   { background: color-mix(in srgb, var(--ok, #2ea043) 18%, transparent); }
  .rp-count.unknown { background: color-mix(in srgb, var(--text-dim, #6b7294) 20%, transparent); }

  .rp-sections { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .rp-section {
    padding: 8px 10px; border-radius: 8px;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .rp-sechead { display: flex; align-items: center; gap: 8px; }
  .rp-sectitle { flex: 1; font-size: .85rem; }
  .rp-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
  .rp-dot.ready   { background: var(--ok, #2ea043); }
  .rp-dot.warn    { background: var(--warn, #f0ad4e); }
  .rp-dot.block   { background: var(--danger, #e5484d); }
  .rp-dot.unknown { background: var(--text-dim, #6b7294); }
  .rp-status { font-size: .72rem; color: var(--text-dim, #6b7294); }
  .rp-status.block { color: var(--danger, #e5484d); }
  .rp-reason { margin: 4px 0 0; font-size: .78rem; font-style: italic; color: var(--text-dim, #6b7294); }

  .rp-item {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    margin-top: 6px; padding: 6px 8px; border-radius: 6px; font-size: .82rem;
  }
  .rp-item.block { background: color-mix(in srgb, var(--danger, #e5484d) 10%, transparent); }
  .rp-item.warn  { background: color-mix(in srgb, var(--warn, #f0ad4e) 10%, transparent); }
  .rp-msg { flex: 1; min-width: 160px; }
  .rp-fix { font-size: .78rem; color: var(--text-dim, #6b7294); width: 100%; }
  .rp-manual { font-size: .76rem; font-style: italic; color: var(--text-dim, #6b7294); }
  .rp-destination {
    width: min(360px, 100%);
    display: flex;
    align-items: center;
    gap: 8px;
    margin-left: auto;
    color: var(--text-dim, #6b7294);
    font-size: .76rem;
  }
  .rp-destination span { flex: 0 0 auto; }
  .rp-destination select {
    min-width: 190px;
    flex: 1 1 auto;
    padding: 6px 9px;
    color: var(--text, inherit);
    border: 1px solid color-mix(in srgb, var(--accent, #6d5efc) 45%, var(--border));
    border-radius: 6px;
    background: var(--bg-elev, #141b2d);
  }

  .rp-passes { font-size: .8rem; }
  .rp-passes summary { cursor: pointer; color: var(--text-dim, #6b7294); }
  .rp-passes ul { margin: 6px 0 0; padding-left: 18px; }
</style>
