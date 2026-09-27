<script>
  // What the agent is doing, while it does it.
  //
  // Replaces two bad shapes at once: the Genie screen, which showed a bare
  // spinner because it discarded every event, and the Chat panel, which
  // rendered up to eighty full-height rows and force-scrolled to the bottom on
  // each one — so reading an earlier step while a run was live was impossible.
  //
  // The rules here:
  //   - steps, not events (a tool call and its result are one line);
  //   - a fixed height with its own scroller, so the page never grows;
  //   - follow the newest step only while the reader is already at the bottom;
  //   - a failed step is visible when collapsed, because that is the one
  //     nobody should have to go looking for.
  import { afterUpdate } from 'svelte'
  import { groupRunEvents, liveActivity, runSummary, eventFullDetail, isPinnedToBottom } from './runevents.js'

  export let events = []
  export let live = false
  /** Open by default while running: the point is to show the work. */
  export let open = live
  export let title = 'What it is doing'
  export let doneTitle = 'How this answer was made'

  let scroller = null
  let pinned = true

  $: steps = groupRunEvents(events)
  $: summary = runSummary(events)
  $: failures = steps.filter((s) => s.failed)
  $: now = live ? liveActivity(events) : ''

  // Follow new steps only if the reader has not scrolled away.
  //
  // Deliberately an update hook and not a reactive statement: writing
  // scrollTop can fire a scroll event, which sets `pinned`, which would
  // re-run a reactive block that writes scrollTop again. Firing only when the
  // step count actually changed cannot loop.
  let lastCount = 0
  afterUpdate(() => {
    if (!scroller || !pinned || steps.length === lastCount) return
    lastCount = steps.length
    scroller.scrollTop = scroller.scrollHeight
  })

  function onScroll() {
    pinned = isPinnedToBottom(scroller)
  }
  function toggle() {
    open = !open
  }
  let expanded = new Set()
  function toggleStep(i) {
    expanded.has(i) ? expanded.delete(i) : expanded.add(i)
    expanded = expanded
  }
  function fullOf(step) {
    return eventFullDetail(step.result || step.call || {})
  }
</script>

{#if steps.length || live}
  <section class="run" class:open>
    <button class="head" type="button" on:click={toggle} aria-expanded={open}>
      <span class="chev" aria-hidden="true">{open ? '▾' : '▸'}</span>
      <span class="name">{live ? title : doneTitle}</span>
      {#if live && now}
        <span class="now"><span class="dot" aria-hidden="true"></span>{now}</span>
      {:else if summary}
        <span class="meta">{summary}</span>
      {/if}
      <!-- A failure stays visible when collapsed: it is the one thing nobody
           should have to open a panel to discover. -->
      {#if !open && failures.length}
        <span class="failed-badge">{failures.length} failed</span>
      {/if}
    </button>

    {#if open}
      <div class="body" bind:this={scroller} on:scroll={onScroll}>
        {#if !steps.length}
          <div class="empty">Nothing has happened yet.</div>
        {/if}
        <ol class="steps">
          {#each steps as step, i (i)}
            <li class="step {step.kind}" class:failed={step.failed} class:pending={!step.done}>
              <span class="bullet" aria-hidden="true"></span>
              <div class="what">
                <button class="step-title" type="button" on:click={() => toggleStep(i)}
                        disabled={!fullOf(step)} aria-expanded={expanded.has(i)}>
                  {step.title}
                </button>
                {#if step.detail}<div class="detail">{step.detail}</div>{/if}
                {#if expanded.has(i) && fullOf(step)}
                  <pre class="full">{fullOf(step)}</pre>
                {/if}
              </div>
            </li>
          {/each}
        </ol>
      </div>
      {#if !pinned && live}
        <button class="jump" type="button" on:click={() => { pinned = true; if (scroller) scroller.scrollTop = scroller.scrollHeight }}>
          Jump to the newest step
        </button>
      {/if}
    {/if}
  </section>
{/if}

<style>
  .run { border: 1px solid var(--sl-line); border-radius: 12px; background: var(--sl-surface); overflow: hidden; }
  .head {
    width: 100%; display: flex; align-items: center; gap: .5rem; padding: .55rem .7rem;
    background: none; border: 0; color: var(--sl-text-dim); font: inherit; font-size: .8rem; cursor: pointer; text-align: left;
  }
  .head:hover { color: var(--sl-text); }
  .chev { color: var(--sl-text-faint); font-size: .7rem; }
  .name { font-weight: 600; }
  .meta, .now { margin-left: auto; color: var(--sl-text-faint); font-size: .72rem; display: flex; align-items: center; gap: .35rem; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--sl-accent); animation: pulse 1.4s ease-in-out infinite; }
  @keyframes pulse { 0%, 100% { opacity: .35 } 50% { opacity: 1 } }
  @media (prefers-reduced-motion: reduce) { .dot { animation: none } }
  .failed-badge { margin-left: .4rem; color: #ff8080; font-size: .7rem; }

  /* The height cap is the whole point: the panel never grows the page. */
  .body { max-height: 260px; overflow-y: auto; padding: .1rem .7rem .5rem; border-top: 1px solid var(--sl-line); }
  .empty { color: var(--sl-text-faint); font-size: .78rem; padding: .5rem 0; }
  .steps { list-style: none; margin: 0; padding: 0; }
  .step { display: flex; gap: .55rem; padding: .3rem 0; align-items: flex-start; }
  .bullet { width: 7px; height: 7px; border-radius: 50%; margin-top: .42rem; flex: none; background: var(--sl-text-faint); }
  .step.tool .bullet { background: var(--sl-accent); }
  .step.model .bullet { background: var(--sl-text-faint); }
  .step.failed .bullet, .step.err .bullet { background: #ff6b6b; }
  .step.pending .bullet { animation: pulse 1.4s ease-in-out infinite; }
  .what { min-width: 0; flex: 1; }
  .step-title { background: none; border: 0; padding: 0; font: inherit; font-size: .78rem; color: var(--sl-text); cursor: pointer; text-align: left; }
  .step-title[disabled] { cursor: default; }
  .step.failed .step-title { color: #ff9c9c; }
  .detail {
    color: var(--sl-text-faint); font-size: .72rem; margin-top: .1rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .full {
    margin: .35rem 0 0; padding: .45rem .55rem; background: var(--sl-bg); border: 1px solid var(--sl-line);
    border-radius: 8px; font-size: .7rem; max-height: 200px; overflow: auto; white-space: pre-wrap; word-break: break-word;
  }
  .jump {
    display: block; width: 100%; padding: .35rem; border: 0; border-top: 1px solid var(--sl-line);
    background: var(--sl-surface-raised); color: var(--sl-accent-ink); font: inherit; font-size: .72rem; cursor: pointer;
  }
</style>
