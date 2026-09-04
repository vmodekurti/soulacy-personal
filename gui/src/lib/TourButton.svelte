<!--
  TourButton.svelte — "Show me around", in the page it is about.

  It used to live in the sidebar, which made it look like a destination rather
  than help with what you are currently looking at. Context-sensitive help
  belongs next to the context.

  Self-contained on purpose: it works out which screen it is on from the route
  and owns its own panel, so a page adds it with one tag and no wiring. That
  matters at twenty-two call sites — anything needing a prop and an event
  handler would be got wrong somewhere, and the wrong page's story is worse
  than no story.
-->
<script>
  import { onMount, onDestroy } from 'svelte'
  import PageTour from './PageTour.svelte'
  import { NAVIGATE_TARGETS } from './studio/fixactions.js'
  import { walkthrough, startWalkthrough } from './walkthrough/store.js'

  /** Override the detected page id. Only needed where the route and the
      screen disagree; every normal page can leave this alone. */
  export let page = ''

  let current = page
  let open = false

  function detect() {
    if (page) { current = page; return }
    const h = (typeof location !== 'undefined' ? location.hash : '') || ''
    const route = h.replace(/^#/, '').split('?')[0]
    current = route || 'dashboard'
  }

  onMount(() => {
    detect()
    window.addEventListener('hashchange', detect)
  })
  onDestroy(() => {
    if (typeof window !== 'undefined') window.removeEventListener('hashchange', detect)
  })

  function act(story) {
    open = false
    const target = NAVIGATE_TARGETS[story?.nextAction]
    if (target) { window.location.hash = target; return }
    if (story?.nextAction === 'open_studio') window.location.hash = '#studio'
  }

  function fullTour() {
    open = false
    const s = $walkthrough
    startWalkthrough(!s.seen && s.resumeIndex > 0 ? 'resume' : 0)
  }
</script>

<button class="tour-btn" type="button" on:click={() => { detect(); open = true }}
        title="What is this screen for?" aria-label="Show me around this screen">
  <span aria-hidden="true">🧭</span> Show me around
</button>

<PageTour page={current} {open}
          on:close={() => open = false}
          on:fulltour={fullTour}
          on:act={(e) => act(e.detail)} />

<style>
  .tour-btn {
    flex: 0 0 auto;
    display: inline-flex; align-items: center; gap: .35rem;
    background: none; color: #7d84ae;
    border: 1px solid #2a2f4a; border-radius: 8px;
    font-size: .78rem; font-weight: 500;
    padding: .34rem .7rem; white-space: nowrap;
  }
  .tour-btn:hover { color: #b3adff; border-color: #6c63ff; background: rgba(108, 99, 255, 0.1); }
</style>
