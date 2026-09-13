import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const read = path => readFileSync(new URL(path, import.meta.url))
const manifest = JSON.parse(read('../../public/manifest.webmanifest'))

function expectOpaquePNG(path, size) {
  const png = read(path)
  expect(png.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a')
  expect(png.readUInt32BE(16)).toBe(size)
  expect(png.readUInt32BE(20)).toBe(size)
  expect(png[24]).toBe(8) // bit depth
  expect(png[25]).toBe(2) // RGB, no alpha channel
  for (let offset = 8; offset < png.length;) {
    expect(png.toString('ascii', offset + 4, offset + 8)).not.toBe('tRNS')
    offset += 12 + png.readUInt32BE(offset)
  }
}

describe('production branding assets', () => {
  it.each([32, 128, 180, 192, 512])('ships a real opaque %spx PNG', size => {
    expectOpaquePNG(`../../public/brand/living-core-blue-v1-${size}.png`, size)
  })
  it('ships a 1024px opaque App Store master', () => {
    expectOpaquePNG('../../../docs/brand/blue-living-core/icon-1024.png', 1024)
  })
  it('uses existing size-matched PNGs for all PWA icons and shortcuts', () => {
    const icons = [...manifest.icons, ...manifest.shortcuts.flatMap(s => s.icons)]
    for (const icon of icons) {
      const size = Number(icon.sizes.split('x')[0])
      expect(icon.type).toBe('image/png')
      expect(icon.src).toMatch(/^\/brand\/living-core-blue-v1-/)
      expectOpaquePNG(`../../public${icon.src}`, size)
    }
    expect(manifest.icons.some(icon => icon.sizes === '512x512' && icon.purpose.includes('maskable'))).toBe(true)
  })
  it('uses PNG favicons and an Apple touch icon rather than the retired SVG', () => {
    const html = read('../../index.html').toString()
    expect(html).toContain('sizes="32x32" href="/brand/living-core-blue-v1-32.png"')
    expect(html).toContain('sizes="180x180" href="/brand/living-core-blue-v1-180.png"')
    expect(html).not.toContain('/icon.svg')
  })
  it('versions the service-worker shell and updates push artwork', () => {
    const worker = read('../../public/sw.js').toString()
    expect(worker).toContain('soulacy-shell-v4-living-core')
    expect(worker).toContain("icon: '/brand/living-core-blue-v1-192.png'")
    expect(worker).not.toContain('/icon.svg')
  })
  it('keeps the marketing and documentation marks identical to the app mark', () => {
    const mark = read('../../public/brand/living-core-blue-v1-128.png')
    expect(read('../../../website/brand/living-core-blue-v1-128.png')).toEqual(mark)
    expect(read('../../../docs/assets/living-core-blue-v1.png')).toEqual(mark)
  })
})
