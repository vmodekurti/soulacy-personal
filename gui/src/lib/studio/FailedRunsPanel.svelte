<script>
  // FailedRunsPanel — Failed runs → self-heal (ST-13 / ST-14).
  //
  // Left: repeated failures collapsed into one row each. The endpoint returns a
  // flat list, so an agent failing the same way every morning produced one row
  // per run — and twelve identical rows tell you less than one row saying
  // "12 times since Tuesday", because the single genuinely different failure is
  // now buried among them.
  //
  // Right: the diagnosis for the selected group — root cause, evidence,
  // recommended fix — plus the two things you can do about it.
  //
  // This panel used to carry a second, parallel review-and-approve flow for a
  // repair PROPOSAL: a verdict line, a field-level diff, a note box and an
  // Approve & apply button. None of it could ever appear. `repair` was only
  // ever assigned null by the page, and `onApply` was never passed at all, so
  // the whole block rendered for nobody. The working review flow lives on the
  // page (the diff strip under this panel), driven by diagnose-run, which
  // returns a workflow with the trace repairs ALREADY applied — there is no
  // proposal left for an apply-repair call to judge. Two implementations of one
  // idea, one of them unreachable, is worse than one; the unreachable one is
  // gone.

  import { groupFailures, categoryCounts, isRetryable, CATEGORY_LABEL, CATEGORY_HINT } from './failuregroup.js'

  export let runs = []            // /studio/failed-runs
  export let diagnosis = null     // /studio/run-diagnosis for the selected run
  export let loading = false
  export let error = ''
  export let busy = false

  export let onSelect = () => {}  // (run) => void — load trace + diagnosis
  export let onRepair = () => {}  // (run) => void — diagnose and propose a fix
  export let onReveal = () => {}  // (nodeId) => void
  export let onRetry = () => {}   // (run) => void — re-run unchanged

  let selectedKey = ''
  let filter = ''

  $: groups = groupFailures(runs)
  $: counts = categoryCounts(groups)
  $: shown = filter ? groups.filter((g) => g.category === filter) : groups
  // Derived only — it must NOT write selectedKey back. Doing so made `selected`
  // and `selectedKey` mutually dependent, which Svelte rejects as a cyclical
  // reactive dependency even though it would have settled at run time.
  //
  // Nothing needs the two to agree: the template compares against `selected`,
  // and `pick()` is the only writer. Leaving a filtered-out key in place is
  // deliberate — clearing the filter restores the user's original selection
  // rather than silently reassigning it to whatever happened to be first.
  $: selected = shown.find((g) => g.key === selectedKey) || shown[0] || null

  function pick(g) {
    selectedKey = g.key
    onSelect(g.latest)
  }
  function when(ms) {
    if (!ms) return ''
    try { return new Date(ms).toLocaleString() } catch (_) { return '' }
  }
</script>

<div class="fr">
  {#if error}
    <div class="fr-error" role="alert">{error}</div>
  {/if}

  {#if loading && !groups.length}
    <p class="fr-muted">Loading failed runs…</p>
  {:else if !groups.length}
    <p class="fr-muted">No failed runs. </p>
  {:else}
    <!-- Filter offers only categories that actually occurred, so a filter can
         never return an empty screen the user has to undo. -->
    {#if Object.keys(counts).length > 1}
      <div class="fr-filters">
        <button class="fr-chip" class:active={!filter} type="button" on:click={() => (filter = '')}>
          All ({groups.reduce((n, g) => n + g.count, 0)})
        </button>
        {#each Object.entries(counts) as [cat, n]}
          <button class="fr-chip" class:active={filter === cat} type="button" on:click={() => (filter = cat)}>
            {CATEGORY_LABEL[cat] || cat} ({n})
          </button>
        {/each}
      </div>
    {/if}

    <div class="fr-cols">
      <ul class="fr-list">
        {#each shown as g (g.key)}
          <li>
            <button class="fr-item" class:sel={selected && g.key === selected.key}
              type="button" on:click={() => pick(g)}>
              <span class="fr-item-head">
                <span class="fr-cat {g.category}">{CATEGORY_LABEL[g.category]}</span>
                {#if g.count > 1}
                  <!-- The count IS the finding: this is systematic, not a blip. -->
                  <span class="fr-count">×{g.count}</span>
                {/if}
              </span>
              <span class="fr-node">{g.node || 'unknown step'}</span>
              <span class="fr-msg">{g.message}</span>
              <span class="fr-when">
                {#if g.count > 1}{g.count} times, latest {when(g.last)}{:else}{when(g.last)}{/if}
              </span>
            </button>
          </li>
        {/each}
      </ul>

      <div class="fr-detail">
        {#if !selected}
          <p class="fr-muted">Select a failure.</p>
        {:else}
          <div class="fr-cat-hint">{CATEGORY_HINT[selected.category]}</div>

          {#if selected.count > 1}
            <p class="fr-repeat">
              Seen {selected.count} times between {when(selected.first)} and {when(selected.last)}.
              Repeated identically, so this is a standing fault rather than a one-off.
            </p>
          {/if}

          {#if diagnosis}
            <!-- `summary` is the one field the server sets on EVERY path,
                 including the "no retained trace" one whose whole payload is
                 this line plus its suggestions. -->
            {#if diagnosis.summary}
              <div class="fr-cause">
                <span class="fr-label">What happened</span>
                <span>{diagnosis.summary}</span>
              </div>
            {/if}
            {#if diagnosis.rootCause}
              <div class="fr-cause">
                <span class="fr-label">Root cause</span>
                <span>{diagnosis.rootCause}</span>
              </div>
            {/if}
            {#if (diagnosis.evidence || []).length}
              <details class="fr-evidence" open>
                <summary>Evidence</summary>
                <ul>{#each diagnosis.evidence as e}<li>{e}</li>{/each}</ul>
              </details>
            {/if}
            {#if diagnosis.nextAction}
              <div class="fr-fix">
                <span class="fr-label">Recommended fix</span>
                <span>{diagnosis.nextAction}</span>
              </div>
            {/if}
            {#if (diagnosis.suggestions || []).length}
              <div class="fr-fix">
                <span class="fr-label">Try this</span>
                <ul class="fr-suggestions">
                  {#each diagnosis.suggestions as sug}<li>{sug}</li>{/each}
                </ul>
              </div>
            {/if}
            {#if diagnosis.failedNode}
              <button class="btn btn-sm" type="button" on:click={() => onReveal(diagnosis.failedNode)}>
                Show “{diagnosis.failedNode}” on the canvas
              </button>
            {/if}
          {:else}
            <p class="fr-muted">No diagnosis for this run.</p>
          {/if}

          <div class="fr-actions">
            <button class="btn btn-sm" type="button" disabled={busy} on:click={() => onRepair(selected.latest)}>
              Propose a repair
            </button>
            {#if isRetryable(selected.category)}
              <!-- Offered ONLY for transient faults. A retry button next to a
                   configuration error invites the user to waste a run. -->
              <button class="btn btn-sm" type="button" disabled={busy} on:click={() => onRetry(selected.latest)}>
                Retry unchanged
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .fr-suggestions { margin: 4px 0 0; padding-left: 18px; }
  .fr-suggestions li { margin: 2px 0; }
  .fr { display: flex; flex-direction: column; gap: 8px; }
  .fr-muted { font-size: .84rem; color: var(--text-dim, #6b7294); }
  .fr-error {
    padding: 8px 10px; border-radius: 6px; font-size: .82rem;
    color: var(--danger, #e5484d);
    background: color-mix(in srgb, var(--danger, #e5484d) 10%, transparent);
  }

  .fr-filters { display: flex; flex-wrap: wrap; gap: 4px; }
  .fr-chip {
    padding: 2px 10px; border-radius: 999px; font-size: .75rem; cursor: pointer;
    background: transparent; color: var(--text-dim, #6b7294);
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .fr-chip.active {
    color: var(--text, inherit);
    background: color-mix(in srgb, var(--accent, #6d5efc) 16%, transparent);
    border-color: color-mix(in srgb, var(--accent, #6d5efc) 40%, transparent);
  }

  .fr-cols { display: grid; grid-template-columns: 280px 1fr; gap: 12px; align-items: start; }
  @media (max-width: 860px) { .fr-cols { grid-template-columns: 1fr; } }

  .fr-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; max-height: 380px; overflow-y: auto; }
  .fr-item {
    width: 100%; text-align: left; cursor: pointer;
    display: flex; flex-direction: column; gap: 2px;
    padding: 8px 10px; border-radius: 8px; color: inherit;
    background: transparent;
    border: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
  }
  .fr-item.sel { border-color: var(--accent, #6d5efc); }
  .fr-item-head { display: flex; align-items: center; gap: 6px; }
  .fr-cat { padding: 1px 7px; border-radius: 999px; font-size: .68rem; background: color-mix(in srgb, var(--text-dim, #6b7294) 20%, transparent); }
  .fr-cat.transient { background: color-mix(in srgb, var(--warn, #f0ad4e) 22%, transparent); }
  .fr-cat.permission, .fr-cat.provider { background: color-mix(in srgb, var(--danger, #e5484d) 20%, transparent); }
  .fr-count { font-size: .7rem; font-weight: 600; color: var(--danger, #e5484d); }
  .fr-node { font-size: .82rem; font-family: var(--mono, monospace); }
  .fr-msg { font-size: .78rem; color: var(--text-dim, #6b7294); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fr-when { font-size: .72rem; color: var(--text-dim, #6b7294); }

  .fr-detail { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .fr-cat-hint { font-size: .82rem; color: var(--text-dim, #6b7294); }
  .fr-repeat {
    margin: 0; padding: 6px 8px; border-radius: 6px; font-size: .8rem;
    background: color-mix(in srgb, var(--warn, #f0ad4e) 10%, transparent);
  }
  .fr-label { font-size: .7rem; text-transform: uppercase; letter-spacing: .04em; color: var(--text-dim, #6b7294); }
  .fr-cause {
    display: flex; flex-direction: column; gap: 2px;
    padding: 8px 10px; border-radius: 6px; font-size: .84rem;
    background: color-mix(in srgb, var(--danger, #e5484d) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--danger, #e5484d) 28%, transparent);
  }
  .fr-fix { display: flex; flex-direction: column; gap: 2px; font-size: .84rem; }
  .fr-evidence { font-size: .82rem; }
  .fr-evidence ul { margin: 4px 0 0; padding-left: 18px; }

  .fr-actions { display: flex; gap: 6px; flex-wrap: wrap; }

</style>
