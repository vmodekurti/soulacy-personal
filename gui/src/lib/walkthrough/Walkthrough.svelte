<!--
  Walkthrough.svelte — the orientation tour overlay.

  It spotlights one sidebar entry at a time, navigates the app to that screen so
  the real page is visible behind the dim, and explains what the screen is for.
  Anchors are looked up by the `data-tour` attribute App.svelte stamps on each
  nav button, via navAnchor() — neither side hand-writes the selector.

  When the anchor cannot be resolved (narrow viewport with the drawer closed, a
  nav entry that no longer exists) the card falls back to the centre of the
  screen rather than pointing at nothing.
-->
<script>
  import { createEventDispatcher, tick } from 'svelte'
  import { walkthrough, nextStep, prevStep, skipWalkthrough, pauseWalkthrough, gotoStep } from './store.js'
  import { walkthroughSteps } from './steps.js'

  const dispatch = createEventDispatcher()

  let rect = null      // spotlight box in viewport coords, or null → centred card
  let cardH = 0
  let cardEl = null
  let lastNav = ''

  $: active = $walkthrough.active
  $: index = $walkthrough.index
  $: step = walkthroughSteps[index] || walkthroughSteps[0]
  $: total = walkthroughSteps.length
  $: isLast = index >= total - 1

  // Drive the app to the screen this step describes, then find its nav anchor.
  $: if (active && step) enterStep(step)
  $: if (!active) { lastNav = ''; rect = null }

  async function enterStep(s) {
    if (s.nav && s.nav !== lastNav) {
      lastNav = s.nav
      dispatch('navigate', s.nav)
    }
    await tick()
    const el = anchorEl(s)
    if (el && el.scrollIntoView) el.scrollIntoView({ block: 'nearest' })
    // One frame so the scroll (and any nav re-render) has settled before we measure.
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(measure)
    else measure()
    focusCard()
  }

  function anchorEl(s) {
    if (!s || !s.anchor || typeof document === 'undefined') return null
    return document.querySelector(`[data-tour="${s.anchor}"]`)
  }

  export function measure() {
    const el = anchorEl(step)
    if (!el || typeof el.getBoundingClientRect !== 'function') { rect = null; return }
    const r = el.getBoundingClientRect()
    const vw = window.innerWidth || 0
    const vh = window.innerHeight || 0
    // Zero-sized (display:none) or pushed off-canvas (mobile drawer closed).
    if (!r.width || !r.height) { rect = null; return }
    if (r.right < 8 || r.bottom < 8 || r.left > vw - 8 || r.top > vh - 8) { rect = null; return }
    rect = { top: r.top, left: r.left, width: r.width, height: r.height }
  }

  async function focusCard() {
    await tick()
    if (cardEl && typeof cardEl.focus === 'function') cardEl.focus({ preventScroll: true })
  }

  // Card placement: to the right of the spotlit nav item, clamped to the
  // viewport so a step near the bottom of a long sidebar is still fully visible.
  const CARD_W = 340
  $: cardStyle = rect
    ? placeBeside(rect, cardH)
    : 'left: 50%; top: 50%; transform: translate(-50%, -50%);'

  function placeBeside(r, h) {
    const vw = window.innerWidth || 1024
    const vh = window.innerHeight || 768
    let left = r.left + r.width + 18
    if (left + CARD_W > vw - 12) left = Math.max(12, r.left - CARD_W - 18)
    const height = h || 220
    let top = r.top + r.height / 2 - height / 2
    top = Math.min(Math.max(top, 12), Math.max(12, vh - height - 12))
    return `left: ${Math.round(left)}px; top: ${Math.round(top)}px;`
  }

  function onKey(e) {
    if (!active) return
    if (e.key === 'Escape')      { e.preventDefault(); pauseWalkthrough() }
    else if (e.key === 'ArrowRight') { e.preventDefault(); nextStep() }
    else if (e.key === 'ArrowLeft')  { e.preventDefault(); prevStep() }
  }

</script>

<!-- Keyboard + viewport handling via svelte:window so the listeners are bound
     and torn down with the component, without an onMount/onDestroy pair. -->
<svelte:window on:keydown={onKey} on:resize={measure} />

{#if active}
  <!-- Swallows clicks on the app while the tour is up. The dim itself comes
       from the spotlight's outer shadow so the highlighted item stays bright. -->
  <div class="wt-scrim" class:plain={!rect} on:click={pauseWalkthrough} on:keydown|stopPropagation
       role="presentation" aria-hidden="true"></div>

  {#if rect}
    <div class="wt-spot"
         style="top:{rect.top - 4}px; left:{rect.left - 4}px; width:{rect.width + 8}px; height:{rect.height + 8}px;"
         aria-hidden="true"></div>
  {/if}

  <div class="wt-card"
       bind:this={cardEl}
       bind:clientHeight={cardH}
       style={cardStyle}
       role="dialog"
       aria-modal="true"
       aria-labelledby="wt-title"
       tabindex="-1">
    <div class="wt-head">
      <span class="wt-count">Step {index + 1} of {total}</span>
      <button class="wt-close" on:click={pauseWalkthrough} aria-label="Close the tour">✕</button>
    </div>

    <h2 id="wt-title">
      {#if step.icon}<span class="wt-icon" aria-hidden="true">{step.icon}</span>{/if}
      {step.title}
    </h2>

    <p class="wt-what">{step.what}</p>
    {#if step.when}<p class="wt-when">{step.when}</p>{/if}

    <div class="wt-bar" aria-hidden="true">
      <div class="wt-bar-fill" style="width: {Math.round(((index + 1) / total) * 100)}%"></div>
    </div>

    <div class="wt-actions">
      <button class="wt-ghost" on:click={skipWalkthrough}>Skip tour</button>
      <span class="wt-spacer"></span>
      {#if index > 0}
        <button class="wt-ghost" on:click={() => gotoStep(0)}>Restart</button>
        <button class="wt-btn" on:click={prevStep}>Back</button>
      {/if}
      <button class="wt-btn primary" on:click={nextStep}>{isLast ? 'Done' : 'Next'}</button>
    </div>
  </div>
{/if}

<style>
  .wt-scrim {
    position: fixed; inset: 0; z-index: 900;
    background: transparent; border: none;
  }
  .wt-scrim.plain { background: rgba(6, 8, 18, 0.72); }

  .wt-spot {
    position: fixed; z-index: 901;
    border-radius: 11px;
    border: 1px solid rgba(139, 133, 255, 0.9);
    box-shadow: 0 0 0 9999px rgba(6, 8, 18, 0.72), 0 0 18px rgba(126, 92, 255, 0.55);
    pointer-events: none;
    transition: top 0.16s ease, left 0.16s ease, width 0.16s ease, height 0.16s ease;
  }

  .wt-card {
    position: fixed; z-index: 902;
    width: 340px; max-width: calc(100vw - 24px);
    background: #141728;
    border: 1px solid #2f3459;
    border-radius: 14px;
    padding: 1rem 1.1rem 0.9rem;
    box-shadow: 0 18px 50px rgba(0, 0, 0, 0.6);
    outline: none;
  }
  .wt-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.35rem; }
  .wt-count { font-size: 0.68rem; letter-spacing: 0.08em; text-transform: uppercase; color: #6f76a0; }
  .wt-close {
    margin-left: auto; background: none; color: #6f76a0;
    font-size: 0.85rem; line-height: 1; padding: 0.2rem 0.3rem; border-radius: 6px;
  }
  .wt-close:hover { background: #1e2238; color: #d4d7ea; }

  .wt-card h2 {
    font-size: 1rem; font-weight: 650; color: #f2f3fb;
    display: flex; align-items: center; gap: 0.45rem; margin-bottom: 0.5rem;
  }
  .wt-icon { font-size: 0.95rem; }

  .wt-what { font-size: 0.855rem; line-height: 1.55; color: #d3d6ec; }
  .wt-when { font-size: 0.8rem; line-height: 1.55; color: #8b91b8; margin-top: 0.5rem; }

  .wt-bar { height: 3px; border-radius: 2px; background: #232741; margin: 0.9rem 0 0.75rem; overflow: hidden; }
  .wt-bar-fill { height: 100%; background: linear-gradient(90deg, #7e5cff, #22c47a); transition: width 0.18s ease; }

  .wt-actions { display: flex; align-items: center; gap: 0.4rem; }
  .wt-spacer { flex: 1; }
  .wt-ghost {
    background: none; color: #767ca6; font-size: 0.78rem;
    padding: 0.35rem 0.4rem; border-radius: 6px;
  }
  .wt-ghost:hover { color: #c8cadf; background: #1e2238; }
  .wt-btn {
    background: #1c2038; color: #dfe2ff; border: 1px solid #2f3459;
    font-size: 0.8rem; font-weight: 600;
    padding: 0.38rem 0.85rem; border-radius: 8px;
  }
  .wt-btn:hover { border-color: #6c63ff; }
  .wt-btn.primary { background: #6c63ff; border-color: #6c63ff; color: #fff; }
  .wt-btn.primary:hover { filter: brightness(1.08); }

  @media (max-width: 768px) {
    .wt-card { width: calc(100vw - 24px); }
  }
</style>
