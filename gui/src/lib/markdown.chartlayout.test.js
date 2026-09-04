// @vitest-environment jsdom
// Chart layout invariants (Cohort G framework fix).
//
// The bug the framework fix addressed was: ECharts title defaulted to top-left
// and legend defaulted to top-right, both anchored in the 0-40px band, so any
// title long enough to reach the legend's start x-coord overlapped. The
// framework fix in themeEChartsOption moved the legend to bottom-center and
// reserved dedicated top/bottom lanes.
//
// These tests are STRUCTURAL — they don't render pixels, they assert that
// title and legend never share a lane in any config themeEChartsOption
// produces. That's the invariant the fix guarantees; if someone reverts to
// legend-at-top defaults, this suite breaks first.
import { describe, it, expect } from 'vitest'
import { themeEChartsOption } from './markdown.js'

// Convenience: bar/line series count controls the legend rendering path
// (single series = no legend, multi = legend). The theme also renders a
// legend for pie/radar regardless of count.
function opt(over = {}) {
  return {
    series: [{ type: 'line', name: 'A', data: [1, 2] }, { type: 'line', name: 'B', data: [3, 4] }],
    ...over,
  }
}

describe('themeEChartsOption title/legend lane invariant', () => {
  it('anchors the default title at the top with a positive top offset', () => {
    const result = themeEChartsOption(opt({ title: { text: 'Portfolio Growth' } }))
    expect(result.title).toBeDefined()
    expect(result.title.top).toBeGreaterThanOrEqual(0)
    // Baseline: top<=16 keeps the title tight to the top edge. If someone
    // sets a wild default (e.g. top: 60) the plot area shrinks.
    expect(result.title.top).toBeLessThanOrEqual(16)
  })

  it('anchors the default legend at the BOTTOM (not top) so it never shares a lane with title', () => {
    const result = themeEChartsOption(opt({ title: { text: 'Portfolio Growth' } }))
    expect(result.legend).toBeDefined()
    // Legend must be bottom-anchored. The old broken default was top:8.
    expect(result.legend.bottom).toBeGreaterThanOrEqual(0)
    // And explicitly must NOT set top (which would put it back in the title lane).
    expect(result.legend.top).toBeUndefined()
  })

  it('reserves grid.top when a title is present', () => {
    const withTitle = themeEChartsOption(opt({ title: { text: 'A' } }))
    const withoutTitle = themeEChartsOption(opt())
    expect(withTitle.grid.top).toBeGreaterThanOrEqual(40) // room for title + padding
    // Without a title we can pack the plot tighter.
    expect(withoutTitle.grid.top).toBeLessThan(withTitle.grid.top)
  })

  it('reserves grid.bottom when a legend is present', () => {
    const multi = themeEChartsOption(opt({ title: { text: 'A' } }))
    // Legend adds bottom lane; grid.bottom must reserve at least ~30px for it.
    expect(multi.grid.bottom).toBeGreaterThanOrEqual(30)
  })

  it('collapses the bottom lane when there is no legend (single-series bar/line)', () => {
    const single = themeEChartsOption({
      title: { text: 'A' },
      series: [{ type: 'bar', name: 'Only', data: [1] }],
    })
    // Single-series: no legend, so grid.bottom stays tight.
    expect(single.legend).toBeUndefined()
    expect(single.grid.bottom).toBeLessThanOrEqual(16)
  })

  it('respects an agent-supplied legend override (Object.assign target wins)', () => {
    const override = themeEChartsOption(opt({
      title: { text: 'A' },
      legend: { top: 8, right: 10 },  // agent explicitly wants legend at top
    }))
    // Object.assign(target=defaults, agent's) means the agent's top wins.
    expect(override.legend.top).toBe(8)
    expect(override.legend.right).toBe(10)
  })

  it('turns on legend scroll pagination so wrap can never eat the plot area', () => {
    const result = themeEChartsOption(opt({ title: { text: 'A' } }))
    expect(result.legend.type).toBe('scroll')
  })

  it('is a no-op for null/undefined input', () => {
    expect(themeEChartsOption(null)).toBeNull()
    expect(themeEChartsOption(undefined)).toBeUndefined()
  })

  it('shows a legend for pie/radar even when there is only one series', () => {
    const pie = themeEChartsOption({
      title: { text: 'Allocation' },
      series: [{ type: 'pie', data: [{ name: 'a', value: 1 }] }],
    })
    expect(pie.legend).toBeDefined()
    expect(pie.legend.bottom).toBeGreaterThanOrEqual(0)
  })
})
