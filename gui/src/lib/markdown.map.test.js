// @vitest-environment jsdom
//
// Map support: a pasted OpenStreetMap or Google Maps link becomes a live map,
// and the embed allowlist that protects the video iframes still protects these.

import { describe, it, expect } from 'vitest'
import { parseMarkdown } from './markdown.js'

describe('parseMarkdown map support', () => {
  it('embeds an OpenStreetMap share link', () => {
    const html = parseMarkdown('https://www.openstreetmap.org/#map=15/51.50722/-0.12750')
    expect(html).toContain('openstreetmap.org/export/embed.html')
    expect(html).toContain('marker=51.50722,-0.12750')
  })

  it('embeds an OSM marker link', () => {
    const html = parseMarkdown('https://www.openstreetmap.org/?mlat=48.8584&mlon=2.2945')
    expect(html).toContain('openstreetmap.org/export/embed.html')
    expect(html).toContain('marker=48.85840,2.29450')
  })

  // A Google place URL carries a coordinate, so it can be shown on OSM without
  // an API key and without Google's cookies.
  it('renders a Google place link on OpenStreetMap', () => {
    const html = parseMarkdown('https://www.google.com/maps/place/Eiffel+Tower/@48.8584,2.2945,17z')
    expect(html).toContain('openstreetmap.org/export/embed.html')
    expect(html).toContain('marker=48.85840,2.29450')
  })

  // A search query has no coordinate to convert, so it falls back to Google's
  // keyless embed form.
  it('falls back to the keyless Google embed for a query with no coordinate', () => {
    const html = parseMarkdown('https://www.google.com/maps?q=British+Museum')
    // &amp; in the assertion because this is HTML, not a bare URL.
    expect(html).toContain('maps.google.com/maps?q=British%20Museum&amp;output=embed')
  })

  it('gives a map its own frame rather than the video one', () => {
    const html = parseMarkdown('https://www.openstreetmap.org/#map=12/40.7128/-74.0060')
    expect(html).toContain('md-map')
    expect(html).not.toContain('md-embed')
  })

  // The security boundary has to hold for the new hosts too.
  it('still drops an arbitrary iframe an agent tries to inject', () => {
    const html = parseMarkdown('<iframe src="https://evil.example.com/steal"></iframe>')
    expect(html).not.toContain('evil.example.com')
  })

  it('does not treat a non-map google link as a map', () => {
    const html = parseMarkdown('https://www.google.com/search?q=weather')
    expect(html).not.toContain('md-map')
  })

  it('leaves a described map link as a link', () => {
    const html = parseMarkdown('[see the venue](https://www.openstreetmap.org/#map=15/51.5/-0.12)')
    expect(html).not.toContain('md-map')
    expect(html).toContain('<a')
  })
})
