// @vitest-environment jsdom
//
// The rest of the media surface: PDF previews and the security boundary that
// makes them safe. The iframe rule is the main thing standing between untrusted
// agent output and the DOM, so every test that widens it is paired with one
// that proves it still holds.

import { describe, it, expect } from 'vitest'
import { parseMarkdown } from './markdown.js'

describe('parseMarkdown PDF preview', () => {
  // jsdom serves pages from http://localhost, so a relative link is same-origin.
  it('previews a PDF this gateway serves', () => {
    const html = parseMarkdown('http://localhost:3000/files/report.pdf')
    expect(html).toContain('md-pdf')
    expect(html).toContain('report.pdf')
  })

  it('previews a relative PDF path', () => {
    const html = parseMarkdown('http://localhost:3000/reports/q3.pdf?v=2')
    expect(html).toContain('md-pdf')
  })

  // Someone else's PDF stays a link. Previewing it would mean pointing an
  // iframe at an arbitrary host on the say-so of untrusted output.
  it('leaves an off-origin PDF as a link', () => {
    const html = parseMarkdown('https://example.com/whitepaper.pdf')
    expect(html).not.toContain('md-pdf')
    expect(html).toContain('<a')
  })
})

describe('the iframe boundary still holds', () => {
  it('drops an arbitrary iframe', () => {
    const html = parseMarkdown('<iframe src="https://evil.example.com/x"></iframe>')
    expect(html).not.toContain('evil.example.com')
  })

  it('drops an iframe dressed up as a video host', () => {
    const html = parseMarkdown('<iframe src="https://youtube.com.evil.example.com/embed/x"></iframe>')
    expect(html).not.toContain('evil.example.com')
  })

  it('drops a javascript: iframe', () => {
    const html = parseMarkdown('<iframe src="javascript:alert(1)"></iframe>')
    expect(html).not.toContain('javascript:')
  })

  it('still strips script tags', () => {
    const html = parseMarkdown('<script>alert(1)</script>hello')
    expect(html).not.toContain('<script')
    expect(html).toContain('hello')
  })

  it('still strips inline event handlers', () => {
    const html = parseMarkdown('<img src="x" onerror="alert(1)">')
    expect(html).not.toContain('onerror')
  })
})
