// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { mount, unmount } from 'svelte'
import BrandMark from './BrandMark.svelte'

let component, target
function render(props = {}) {
  target = document.createElement('div')
  document.body.append(target)
  component = mount(BrandMark, { target, props })
  return target.querySelector('img')
}
afterEach(async () => {
  if (component) await unmount(component)
  target?.remove()
})

describe('Living Core brand mark', () => {
  it('is decorative beside live wordmark text, with fixed dimensions', () => {
    const image = render()
    expect(image.getAttribute('src')).toBe('/brand/living-core-blue-v1-128.png')
    expect(image.alt).toBe('')
    expect(image.getAttribute('aria-hidden')).toBe('true')
    expect(image.width).toBe(32)
    expect(image.height).toBe(32)
    expect(image.draggable).toBe(false)
  })
  it('has a readable name when shown without a wordmark', () => {
    const image = render({ alt: 'Soulacy' })
    expect(image.alt).toBe('Soulacy')
    expect(image.hasAttribute('aria-hidden')).toBe(false)
  })
  it.each([28, 30, 32, 38])('keeps the %spx presentation square', size => {
    const image = render({ size })
    expect(image.width).toBe(size)
    expect(image.height).toBe(size)
    expect(image.style.width).toBe(`${size}px`)
    expect(image.style.height).toBe(`${size}px`)
  })
})
