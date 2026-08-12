// @vitest-environment jsdom
//
// Video support in the shared markdown renderer: direct video-file links become
// <video> players, YouTube/Vimeo links become whitelisted <iframe> embeds, and
// any non-whitelisted iframe an agent tries to inject is dropped.

import { describe, it, expect } from 'vitest'
import { parseMarkdown } from './markdown.js'

describe('parseMarkdown video support', () => {
  it('turns a bare direct video link into a <video> player', () => {
    const html = parseMarkdown('https://example.com/clip.mp4')
    expect(html).toContain('<video')
    expect(html).toContain('controls')
    expect(html).toContain('src="https://example.com/clip.mp4"')
    expect(html).not.toContain('<iframe')
  })

  it('handles a video URL with query/hash', () => {
    const html = parseMarkdown('https://cdn.example.com/a/b.webm?token=xyz#t=10')
    expect(html).toContain('<video')
    expect(html).toContain('b.webm?token=xyz#t=10')
  })

  it('embeds a YouTube watch link as a whitelisted iframe', () => {
    const html = parseMarkdown('https://www.youtube.com/watch?v=dQw4w9WgXcQ')
    expect(html).toContain('<iframe')
    expect(html).toContain('https://www.youtube.com/embed/dQw4w9WgXcQ')
  })

  it('embeds a youtu.be short link', () => {
    const html = parseMarkdown('https://youtu.be/dQw4w9WgXcQ')
    expect(html).toContain('https://www.youtube.com/embed/dQw4w9WgXcQ')
  })

  it('embeds a Vimeo link', () => {
    const html = parseMarkdown('https://vimeo.com/123456789')
    expect(html).toContain('https://player.vimeo.com/video/123456789')
  })

  it('does not convert a normal (non-video) link', () => {
    const html = parseMarkdown('https://example.com/article')
    expect(html).not.toContain('<video')
    expect(html).not.toContain('<iframe')
    expect(html).toContain('<a')
  })

  it('does not eat a link with custom text', () => {
    const html = parseMarkdown('[watch here](https://example.com/clip.mp4)')
    expect(html).not.toContain('<video')
    expect(html).toContain('watch here')
  })

  it('drops a raw iframe pointing at an untrusted host', () => {
    const html = parseMarkdown('<iframe src="https://evil.example.com/x"></iframe>')
    expect(html).not.toContain('evil.example.com')
  })
})

describe('parseMarkdown audio support', () => {
  it('turns a bare audio link into an <audio> player', () => {
    const html = parseMarkdown('https://example.com/overview.mp3')
    expect(html).toContain('<audio')
    expect(html).toContain('controls')
    expect(html).toContain('src="https://example.com/overview.mp3"')
    expect(html).not.toContain('<video')
  })

  it('handles the formats a generated recording actually arrives in', () => {
    for (const ext of ['wav', 'm4a', 'aac', 'flac', 'oga', 'opus', 'weba']) {
      const html = parseMarkdown(`https://example.com/clip.${ext}`)
      expect(html, ext).toContain('<audio')
    }
  })

  it('handles an audio URL with query/hash', () => {
    const html = parseMarkdown('https://cdn.example.com/a/b.m4a?token=xyz#t=30')
    expect(html).toContain('<audio')
    expect(html).toContain('b.m4a?token=xyz#t=30')
  })

  // .ogg is ambiguous by extension. Video is checked first, so a video/ogg file
  // keeps its picture rather than becoming an audio-only player.
  it('keeps .ogg as video rather than silently dropping the picture', () => {
    const html = parseMarkdown('https://example.com/clip.ogg')
    expect(html).toContain('<video')
    expect(html).not.toContain('<audio')
  })

  // Same rule as video: only bare links become players, so a link the author
  // wrapped around words stays a link.
  it('leaves a described audio link as a link', () => {
    const html = parseMarkdown('[listen to the briefing](https://example.com/a.mp3)')
    expect(html).not.toContain('<audio')
    expect(html).toContain('<a')
  })
})
