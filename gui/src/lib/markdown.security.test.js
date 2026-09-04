// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import { parseMarkdown, sanitizeRenderedSVG, themeEChartsOption } from './markdown.js'

describe('rich rendering security', () => {
  it.each([
    ['raw HTML', '<img src=x onerror="alert(1)">', 'onerror'],
    ['SVG', '<svg><script>alert(1)</script><a href="javascript:alert(1)">x</a></svg>', 'javascript:'],
    ['link', '[x](javascript:alert(1))', 'javascript:'],
    ['KaTeX', '$\\href{javascript:alert(1)}{x}$', 'javascript:'],
  ])('sanitizes %s payloads', (_kind, input, forbidden) => {
    expect(parseMarkdown(input).toLowerCase()).not.toContain(forbidden)
  })

  it('sanitizes Mermaid SVG after rendering', () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><iframe src="https://evil.test"></iframe></foreignObject><script>alert(1)</script><text>safe</text></svg>'
    const clean = sanitizeRenderedSVG(svg)
    expect(clean).toContain('safe')
    expect(clean).not.toContain('foreignObject')
    expect(clean).not.toContain('<script')
    expect(clean).not.toContain('<iframe')
  })

  it('forces ECharts tooltips onto canvas and removes formatter/CSS injection', () => {
    const option = themeEChartsOption({
      series: [{ type: 'line', data: [1, 2] }],
      tooltip: { formatter: '<img onerror=alert(1)>', extraCssText: 'background:url(https://evil.test)' },
    })
    expect(option.tooltip.renderMode).toBe('richText')
    expect(option.tooltip.formatter).toBeUndefined()
    expect(option.tooltip.extraCssText).toBeUndefined()
  })
})
