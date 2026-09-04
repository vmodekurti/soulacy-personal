// @vitest-environment jsdom
//
// Image layout runs in richRenderer, not parseMarkdown, because it needs a real
// DOM: grouping is decided by where the images sit relative to each other, and
// zooming needs a click handler.
//
// Three images used to stack vertically at full width — three screens of
// scrolling for one answer — and a dense chart could not be read at bubble
// width with no way to enlarge it.

import { describe, it, expect, afterEach } from 'vitest'
import { parseMarkdown, richRenderer } from './markdown.js'

// richRenderer defers to a microtask so Svelte can inject {@html} first.
async function render(md) {
  const node = document.createElement('div')
  node.className = 'markdown-body'
  node.innerHTML = parseMarkdown(md)
  document.body.appendChild(node)
  const action = richRenderer(node, md)
  await Promise.resolve()
  await Promise.resolve()
  return { node, action }
}

afterEach(() => { document.body.innerHTML = '' })

describe('image gallery', () => {
  it('groups a run of images into one grid', async () => {
    const { node } = await render(
      '![a](https://e.com/1.png)\n\n![b](https://e.com/2.png)\n\n![c](https://e.com/3.png)')
    const galleries = node.querySelectorAll('.md-gallery')
    expect(galleries).toHaveLength(1)
    expect(galleries[0].querySelectorAll('img')).toHaveLength(3)
  })

  it('leaves a single image alone', async () => {
    const { node } = await render('![only](https://e.com/1.png)')
    expect(node.querySelector('.md-gallery')).toBeNull()
    expect(node.querySelector('img')).not.toBeNull()
  })

  // An image the author put inline with words belongs where they put it.
  it('does not pull an inline image out of its sentence', async () => {
    const { node } = await render(
      'here is one ![a](https://e.com/1.png)\n\nand another ![b](https://e.com/2.png)')
    expect(node.querySelector('.md-gallery')).toBeNull()
  })

  it('starts a new group when prose interrupts the run', async () => {
    const { node } = await render(
      '![a](https://e.com/1.png)\n\n![b](https://e.com/2.png)\n\nsome words\n\n![c](https://e.com/3.png)')
    const galleries = node.querySelectorAll('.md-gallery')
    expect(galleries).toHaveLength(1)
    expect(galleries[0].querySelectorAll('img')).toHaveLength(2)
  })
})

describe('image zoom', () => {
  it('opens a full-size view on click and closes it again', async () => {
    const { node } = await render('![chart](https://e.com/chart.png)')
    const img = node.querySelector('img')
    expect(img.classList.contains('md-zoomable')).toBe(true)

    img.click()
    const box = document.querySelector('.md-lightbox')
    expect(box).not.toBeNull()
    expect(box.querySelector('img').getAttribute('src')).toBe('https://e.com/chart.png')

    box.click()
    expect(document.querySelector('.md-lightbox')).toBeNull()
  })

  it('closes on Escape, so it is not a trap for keyboard users', async () => {
    const { node } = await render('![chart](https://e.com/chart.png)')
    node.querySelector('img').click()
    expect(document.querySelector('.md-lightbox')).not.toBeNull()

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(document.querySelector('.md-lightbox')).toBeNull()
  })

  it('carries the alt text through for screen readers', async () => {
    const { node } = await render('![revenue by quarter](https://e.com/c.png)')
    node.querySelector('img').click()
    const box = document.querySelector('.md-lightbox')
    expect(box.getAttribute('aria-label')).toBe('revenue by quarter')
  })
})

describe('data tables render into the DOM', () => {
  it('builds a real table from a csv fence', async () => {
    const { node } = await render('```csv\nticker,price\nMU,861.0\nSTX,800.99\n```')
    const table = node.querySelector('table.md-data-table')
    expect(table).not.toBeNull()
    expect(table.querySelectorAll('thead th')).toHaveLength(2)
    expect(table.querySelectorAll('tbody tr')).toHaveLength(2)
  })

  it('right-aligns numeric cells', async () => {
    const { node } = await render('```csv\nname,v\nMU,861.0\n```')
    const cells = node.querySelectorAll('tbody td')
    expect(cells[0].className).not.toContain('num')
    expect(cells[1].className).toContain('num')
  })

  // Cell values are agent-authored. They go in as text, never as markup.
  it('does not interpret a cell as HTML', async () => {
    const { node } = await render('```csv\na\n<img src=x onerror=alert(1)>\n```')
    const td = node.querySelector('tbody td')
    expect(td.querySelector('img')).toBeNull()
    expect(td.textContent).toContain('<img')
  })

  it('leaves an unparseable fence as source rather than blanking it', async () => {
    const { node } = await render('```csv\n\n```')
    expect(node.querySelector('table.md-data-table')).toBeNull()
  })
})
