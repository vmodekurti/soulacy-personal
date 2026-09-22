<script>
  // Renders the gateway's presentation blocks (#199): the right element for
  // the content — metric tiles, a comparison grid with the best row marked, a
  // chart, a timeline, link cards, a checklist — and markdown for the rest.
  // `compact` shows only the first blocks (a card); the full set is for the
  // expanded card and the story viewer.
  import { onMount, onDestroy } from 'svelte'
  import { parseMarkdown, richRenderer, themeEChartsOption } from './markdown.js'
  export let blocks = []
  export let compact = false

  // Compact = a card: the two most telling blocks, typed ones first (a grid
  // and its chart beat a paragraph), in their original order.
  function pick(bs) {
    const typed = bs.filter(b => b.kind !== 'markdown')
    const chosen = (typed.length ? typed : bs).slice(0, 2)
    return bs.filter(b => chosen.includes(b))
  }
  $: shown = compact ? pick(blocks) : blocks
  $: hidden = compact ? Math.max(0, blocks.length - shown.length) : 0

  // Charts draw with ECharts, themed like the rest of the app.
  let charts = new Map()
  function chart(node, block) {
    let inst = null
    let echarts = null
    const draw = async () => {
      if (!echarts) echarts = await import('echarts')
      if (!inst) inst = echarts.init(node, null, { renderer: 'canvas' })
      const c = block.chart || {}
      const palette = [tokenColor(node, '--sl-accent', '#3fd1db'), tokenColor(node, '--sl-mango', '#ffb020'), tokenColor(node, '--sl-coral', '#ff5c72'), tokenColor(node, '--sl-leaf', '#37b46a')]
      inst.setOption(themeEChartsOption({
        color: palette,
        grid: { left: 8, right: 8, top: 28, bottom: 8, containLabel: true },
        tooltip: { trigger: 'axis' },
        legend: (c.series || []).length > 1 ? { top: 0 } : undefined,
        xAxis: { type: 'category', data: c.x || [], axisLabel: { interval: 0, rotate: (c.x || []).some(x => String(x).length > 8) ? 20 : 0 } },
        yAxis: { type: 'value', scale: true },
        series: (c.series || []).map(s => ({ name: s.name, type: c.type === 'line' ? 'line' : 'bar', data: s.values, smooth: true, barMaxWidth: 34 })),
      }))
      inst.resize()
    }
    draw()
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => inst && inst.resize()) : null
    ro && ro.observe(node)
    return { update(b) { block = b; draw() }, destroy() { ro && ro.disconnect(); inst && inst.dispose() } }
  }
  function isNumeric(block, col) { return (block.numeric || []).includes(col) }
  // On a card there is no room for six columns: keep the first text column
  // (the thing being compared) and the numbers; everything is there once
  // the card is opened.
  function visibleColumns(block) {
    const cols = block.columns || []
    if (!compact || cols.length <= 3) return cols.map((c, i) => i)
    const idx = []
    const firstText = cols.findIndex((c, i) => !isNumeric(block, c) && !/^(#|no\.?|rank|option)$/i.test(c.trim()))
    if (firstText >= 0) idx.push(firstText)
    cols.forEach((c, i) => { if (isNumeric(block, c) && !idx.includes(i)) idx.push(i) })
    if (idx.length < 2) cols.forEach((c, i) => { if (idx.length < 3 && !idx.includes(i)) idx.push(i) })
    return idx.sort((a, b) => a - b)
  }
  function tokenColor(node, name, fallback) {
    try { const v = getComputedStyle(node).getPropertyValue(name).trim(); return v || fallback } catch { return fallback }
  }
  function host(u) { try { return new URL(u).hostname.replace(/^www\./, '') } catch { return u } }
</script>

<div class="pres">
  {#each shown as b, i (i)}
    {#if b.kind === 'metrics'}
      <div class="metrics" style="--n:{Math.min(4, b.items.length)}">
        {#each b.items as it}
          <div class="metric"><div class="v">{it.value}</div><div class="l">{it.label}</div>{#if it.hint && !compact}<div class="h">{it.hint}</div>{/if}</div>
        {/each}
      </div>
    {:else if b.kind === 'comparison'}
      {#if b.title}<div class="bt">{b.title}</div>{/if}
      <div class="grid-scroll"><table class="cmp">
        <thead><tr>{#each visibleColumns(b) as ci}<th class:num={isNumeric(b, b.columns[ci])}>{b.columns[ci]}</th>{/each}</tr></thead>
        <tbody>
          {#each b.rows as r, ri}
            <tr class:best={b.best && b.best.row === ri}>
              {#each visibleColumns(b) as ci}<td class:num={isNumeric(b, b.columns[ci])}>{r[ci]}{#if b.best && b.best.row === ri && b.columns[ci] === b.best.column}<span class="pick"> ✓</span>{/if}</td>{/each}
            </tr>
          {/each}
        </tbody>
      </table></div>
    {:else if b.kind === 'chart'}
      {#if b.title && !compact}<div class="bt">{b.title}</div>{/if}
      <div class="chart" use:chart={b} aria-label="{b.title || 'chart'}"></div>
    {:else if b.kind === 'timeline'}
      {#if b.title}<div class="bt">{b.title}</div>{/if}
      <ul class="timeline">{#each b.items as it}<li><time>{it.at}</time><span>{it.title}{#if it.detail} <em>{it.detail}</em>{/if}</span></li>{/each}</ul>
    {:else if b.kind === 'links'}
      {#if b.title}<div class="bt">{b.title}</div>{/if}
      <div class="links">{#each b.items as it}<a class="link" href={it.url} target="_blank" rel="noopener"><span class="lt">{it.title}</span><span class="lh">{host(it.url)} ↗</span></a>{/each}</div>
    {:else if b.kind === 'checklist'}
      {#if b.title}<div class="bt">{b.title}</div>{/if}
      <ul class="checks">{#each b.items as it}<li class:done={it.done}><span class="box">{it.done ? '✓' : ''}</span>{it.label}</li>{/each}</ul>
    {:else}
      <div class="markdown-body md" use:richRenderer={b.text}>{@html parseMarkdown(b.text || '')}</div>
    {/if}
  {/each}
  {#if hidden > 0}<div class="more-hint">+{hidden} more</div>{/if}
</div>

<style>
  .pres { display: grid; gap: 10px; font-size: 13.5px; }
  .bt { font-weight: 600; font-size: 12px; color: var(--sl-text-faint); text-transform: uppercase; letter-spacing: .06em; }
  .metrics { display: grid; grid-template-columns: repeat(var(--n), minmax(0, 1fr)); gap: 8px; }
  .metric { background: var(--sl-surface); border: 1px solid var(--sl-line); border-radius: 10px; padding: 10px 12px; min-width: 0; }
  .metric .v { font-size: 20px; font-weight: 700; letter-spacing: -.01em; font-variant-numeric: tabular-nums; color: var(--sl-text); overflow-wrap: anywhere; }
  .metric .l { font-size: 12px; color: var(--sl-text-dim); margin-top: 2px; }
  .metric .h { font-size: 11px; color: var(--sl-text-faint); margin-top: 2px; }
  .grid-scroll { overflow-x: auto; border: 1px solid var(--sl-line); border-radius: 10px; }
  .cmp { border-collapse: collapse; width: 100%; font-size: 12.5px; }
  .cmp th, .cmp td { padding: 7px 10px; text-align: left; border-bottom: 1px solid var(--sl-line); white-space: nowrap; }
  .cmp th { font-weight: 600; color: var(--sl-text-dim); background: var(--sl-surface); font-size: 11px; text-transform: uppercase; letter-spacing: .04em; }
  .cmp tr:last-child td { border-bottom: 0; }
  .cmp .num { text-align: right; font-variant-numeric: tabular-nums; }
  .cmp tr.best td { background: var(--sl-accent-soft); font-weight: 600; }
  .pick { color: var(--sl-accent-ink); }
  .chart { height: 180px; width: 100%; }
  .timeline { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .timeline li { display: grid; grid-template-columns: 64px 1fr; gap: 10px; align-items: baseline; }
  .timeline time { color: var(--sl-text-faint); font-variant-numeric: tabular-nums; font-size: 12.5px; }
  .timeline em { color: var(--sl-text-faint); font-style: normal; }
  .links { display: grid; gap: 6px; }
  .link { display: flex; justify-content: space-between; gap: 10px; text-decoration: none; padding: 9px 12px; border: 1px solid var(--sl-line); border-radius: 10px; background: var(--sl-surface); color: var(--sl-text); }
  .link:hover { border-color: var(--sl-accent); }
  .lt { font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lh { color: var(--sl-accent-ink); font-size: 12px; flex: none; }
  .checks { list-style: none; margin: 0; padding: 0; display: grid; gap: 5px; }
  .checks li { display: flex; gap: 8px; align-items: center; }
  .checks .box { width: 16px; height: 16px; border: 1.5px solid var(--sl-text-faint); border-radius: 4px; display: grid; place-items: center; font-size: 11px; flex: none; }
  .checks li.done .box { background: var(--sl-leaf); border-color: var(--sl-leaf); color: #fff; }
  .checks li.done { color: var(--sl-text-dim); }
  .md { color: var(--sl-text-dim); line-height: 1.45; }
  .md :global(p) { margin: 0 0 .5em; }
  .md :global(pre) { overflow-x: auto; }
  .more-hint { color: var(--sl-text-faint); font-size: 12px; }
</style>
