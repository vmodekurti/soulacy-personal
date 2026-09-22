<script>
  // A story: one agent, its latest results as slides. Progress bars on top,
  // tap the right half to advance and the left to go back, arrows on a
  // keyboard, Escape or the ✕ to close. Auto-advances while nothing is
  // hovered or held. (soulacy-personal #191, phase 3)
  import { createEventDispatcher, onMount, onDestroy } from 'svelte'
  import { parseMarkdown, richRenderer } from './markdown.js'
  export let stories = []      // [{ id, label, glyph, hue, slides: [{ id, at, body, title }] }]
  export let index = 0         // which story
  export let slideMs = 7000
  const dispatch = createEventDispatcher()

  let slide = 0
  let paused = false
  let timer = null
  let started = Date.now()

  $: story = stories[index] || { slides: [] }
  $: slides = story.slides || []
  $: current = slides[slide] || null

  function restart() { started = Date.now(); clearInterval(timer); timer = setInterval(tick, 100) }
  function tick() {
    if (paused || !slides.length) { started += 100; return }
    if (Date.now() - started >= slideMs) next()
  }
  function next() {
    if (slide < slides.length - 1) { slide += 1; started = Date.now(); return }
    if (index < stories.length - 1) { index += 1; slide = 0; started = Date.now(); return }
    dispatch('close')
  }
  function prev() {
    if (slide > 0) { slide -= 1; started = Date.now(); return }
    if (index > 0) { index -= 1; slide = Math.max(0, (stories[index].slides || []).length - 1); started = Date.now(); return }
    started = Date.now()
  }
  function tap(e) {
    const r = e.currentTarget.getBoundingClientRect()
    if (e.clientX - r.left < r.width / 3) prev(); else next()
  }
  function key(e) {
    if (e.key === 'Escape') dispatch('close')
    else if (e.key === 'ArrowRight' || e.key === ' ') { e.preventDefault(); next() }
    else if (e.key === 'ArrowLeft') prev()
  }
  function relative(iso) {
    const t = new Date(iso).getTime(); if (!t) return ''
    const s = Math.max(0, (Date.now() - t) / 1000)
    if (s < 3600) return `${Math.max(1, Math.floor(s / 60))}m`
    if (s < 86400) return `${Math.floor(s / 3600)}h`
    return `${Math.floor(s / 86400)}d`
  }
  onMount(() => { restart(); window.addEventListener('keydown', key) })
  onDestroy(() => { clearInterval(timer); if (typeof window !== 'undefined') window.removeEventListener('keydown', key) })
</script>

<div class="viewer" role="dialog" aria-modal="true" aria-label="{story.label} story"
     on:mouseenter={() => paused = true} on:mouseleave={() => paused = false}
     on:pointerdown={() => paused = true} on:pointerup={() => paused = false}>
  <div class="progress" aria-hidden="true">
    {#each slides as s, i (s.id)}
      <i class:done={i < slide} class:now={i === slide}><b style="animation-duration:{slideMs}ms" class:paused></b></i>
    {/each}
  </div>
  <div class="head">
    <span class="av" style="--h:{story.hue ?? 190}">{story.glyph}</span>
    <div class="who"><b>{story.label}</b>{#if current}<span>{relative(current.at)}</span>{/if}</div>
    <span class="count">{slides.length ? slide + 1 : 0} / {slides.length}</span>
    <button class="close" on:click={() => dispatch('close')} aria-label="Close">✕</button>
  </div>
  <div class="body" on:click={tap} role="presentation">
    {#if current}
      {#if current.title}<div class="title">{current.title}</div>{/if}
      <div class="md markdown-body" use:richRenderer={current.body}>{@html parseMarkdown(current.body)}</div>
    {:else}
      <div class="empty">Nothing from {story.label} yet.</div>
    {/if}
  </div>
  <div class="foot">
    <button class="pill" on:click={() => dispatch('reply', { story, slide: current })}>Reply to {story.label}</button>
  </div>
</div>

<style>
  .viewer { position: fixed; inset: 0; z-index: 200; display: grid; grid-template-rows: auto auto 1fr auto; color: #fff;
    background: linear-gradient(180deg, #0b1b2b, #123a52 60%, #1c5a6e); }
  .progress { display: flex; gap: 4px; padding: 12px 10px 0; }
  .progress i { flex: 1; height: 2.5px; background: rgba(255,255,255,.3); border-radius: 2px; overflow: hidden; }
  .progress i b { display: block; height: 100%; width: 0; background: #fff; }
  .progress i.done b { width: 100%; }
  .progress i.now b { animation: fill linear forwards; }
  .progress i.now b.paused { animation-play-state: paused; }
  @keyframes fill { to { width: 100%; } }
  .head { display: flex; align-items: center; gap: 10px; padding: 10px 14px; }
  .av { width: 34px; height: 34px; border-radius: 50%; display: grid; place-items: center; font-weight: 700; font-size: 13px;
    background: linear-gradient(135deg, hsl(var(--h) 70% 45%), hsl(calc(var(--h) + 30) 80% 62%)); }
  .who { display: grid; line-height: 1.2; }
  .who span { color: rgba(255,255,255,.7); font-size: 12px; }
  .count { margin-left: auto; color: rgba(255,255,255,.7); font-size: 12px; font-variant-numeric: tabular-nums; }
  .close { background: none; border: 0; color: #fff; font-size: 20px; cursor: pointer; padding: 4px 6px; }
  .body { overflow: auto; padding: 8px 20px 12px; cursor: pointer; }
  .title { font-size: 26px; font-weight: 700; line-height: 1.15; letter-spacing: -.01em; margin-bottom: 10px; text-wrap: balance; }
  .md { font-size: 16px; line-height: 1.5; max-width: 60ch; }
  .md :global(a) { color: #a7ecf1; }
  .md :global(h1), .md :global(h2), .md :global(h3) { color: #fff; }
  .md :global(table) { font-size: 14px; }
  .md :global(pre) { overflow-x: auto; }
  .md :global(img) { max-width: 100%; }
  .empty { color: rgba(255,255,255,.7); padding: 40px 0; text-align: center; }
  .foot { padding: 10px 14px calc(14px + env(safe-area-inset-bottom)); }
  .pill { width: 100%; border: 1px solid rgba(255,255,255,.45); background: rgba(255,255,255,.08); color: #fff; border-radius: 999px; padding: 10px 14px; font: inherit; font-size: 14px; cursor: pointer; }
  @media (prefers-reduced-motion: reduce) { .progress i.now b { animation: none; width: 50%; } }
</style>
