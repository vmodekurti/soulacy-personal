// markdown.js — rich rendering for chat bubbles.
//
// Turns an agent's plain-text reply into formatted, SAFE HTML with:
//   • GFM markdown (bold/italics/lists/tables/links/images)
//   • LaTeX math via KaTeX  ( \(…\) $…$ inline, \[…\] $$…$$ block )
//   • syntax-highlighted fenced code via highlight.js
//   • Mermaid charts/diagrams from ```mermaid / ```xychart fences
//   • inline players for linked media: video files, YouTube/Vimeo embeds,
//     audio files (a generated podcast plays in the bubble rather than arriving
//     as a download link), and OpenStreetMap/Google Maps links as live maps
//   • tables from ```csv / ```tsv / ```data fences
//   • image galleries: a run of images becomes a grid, and any image zooms
//   • inline preview for a PDF this gateway serves (an agent's own report)
//   • interactive data charts (Apache ECharts) from a ```chart JSON fence:
//       ```chart
//       { "xAxis": {"type":"category","data":[...]},
//         "yAxis": {"type":"value"},
//         "series": [{ "type":"line", "data":[...] }] }
//       ```
//     The JSON is an ECharts `option`, parsed with JSON.parse (no eval) so it
//     can't smuggle in code, then restyled to a modern theme before rendering.
//
// Two exports:
//   parseMarkdown(text) -> sanitized HTML string (safe to use with {@html}).
//   richRenderer(node)  -> Svelte action: after the HTML mounts, it highlights
//                          code blocks and replaces mermaid fences with SVG.
//
// SECURITY: agent output is untrusted. marked passes raw HTML through, so every
// parsed string is run through DOMPurify before it touches the DOM, and Mermaid
// runs in securityLevel:'strict' (no embedded HTML / click handlers). The only
// Mermaid output is sanitized a second time after render. Mermaid parses
// agent-authored diagram syntax, so its generated SVG is not a trust boundary.
//
// Heavy libs (mermaid, katex, highlight.js) are bundled eagerly by project
// decision; rendering work is deferred to a microtask so a long history doesn't
// block the main thread on mount.

import { marked } from 'marked'
import markedKatex from 'marked-katex-extension'
import DOMPurify from 'dompurify'
import { tableFromFence } from './datatable.js'
import hljs from 'highlight.js'
import mermaid from 'mermaid'
import * as echarts from 'echarts'

import 'katex/dist/katex.min.css'
import 'highlight.js/styles/github-dark.css'
import './markdown.css'

// ── marked configuration (once) ──────────────────────────────────────────────
marked.setOptions({ gfm: true, breaks: true })
marked.use(
  markedKatex({
    throwOnError: false, // a bad equation renders a small error, never crashes the message
    output: 'html', // HTML-only output is simpler + safer to sanitize than MathML
    nonStandard: true, // also accept single-$ inline math
  }),
)

// Force every link to open safely in a new tab (agent links are untrusted).
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer nofollow')
  }
})

// Video support (untrusted content, so defense-in-depth). We let <video>/<source>
// through for direct file playback and <iframe> ONLY for known video-embed hosts.
// Any iframe whose src is not a whitelisted embed URL is dropped — an agent can't
// smuggle an arbitrary iframe past this even though the tag is allowed.
const VIDEO_FILE_RE = /\.(mp4|webm|ogg|ogv|mov|m4v)(\?[^#\s]*)?(#[^\s]*)?$/i
// Audio gets its own player. Without this an agent that produced a recording —
// a NotebookLM audio overview, a generated podcast, a voice note — could only
// offer a link to download, which is a worse answer than the one it actually
// produced. Note .ogg appears in both lists: it is ambiguous by extension, and
// video is checked first so a video/ogg file keeps its picture.
const AUDIO_FILE_RE = /\.(mp3|wav|m4a|aac|flac|oga|opus|weba)(\?[^#\s]*)?(#[^\s]*)?$/i
const PDF_FILE_RE = /\.pdf(\?[^#\s]*)?(#[^\s]*)?$/i
const EMBED_HOST_RE = /^https:\/\/(www\.)?(youtube\.com\/embed\/|player\.vimeo\.com\/video\/|openstreetmap\.org\/export\/embed\.html|maps\.google\.com\/maps\?)/i
// A PDF the agent itself produced is served by this gateway, so it can be
// previewed without widening the iframe policy: same-origin content is already
// as trusted as the page doing the rendering. A PDF on someone else's origin
// stays a link — the rule that an agent cannot point an iframe at an arbitrary
// host is the main thing standing between untrusted output and the DOM, and a
// document preview is not worth spending it.
function isSameOrigin(href) {
  try {
    if (typeof window === 'undefined' || !window.location) return false
    return new URL(href, window.location.href).origin === window.location.origin
  } catch (_) {
    return false
  }
}

DOMPurify.addHook('uponSanitizeElement', (node, data) => {
  if (data.tagName !== 'iframe') return
  const src = (node.getAttribute && node.getAttribute('src')) || ''
  if (!EMBED_HOST_RE.test(src) && !isSameOrigin(src)) {
    // Not a trusted embed and not our own origin — remove the element entirely.
    if (node.parentNode) node.parentNode.removeChild(node)
  }
})

// Map a YouTube / Vimeo watch URL to its privacy-friendly embed URL, or null if
// the URL isn't a recognised video host.
function toEmbedURL(href) {
  try {
    const u = new URL(href)
    const host = u.hostname.replace(/^www\./, '')
    if (host === 'youtu.be') {
      const id = u.pathname.slice(1)
      if (id) return `https://www.youtube.com/embed/${encodeURIComponent(id)}`
    }
    if (host === 'youtube.com' || host === 'm.youtube.com') {
      if (u.pathname === '/watch') {
        const id = u.searchParams.get('v')
        if (id) return `https://www.youtube.com/embed/${encodeURIComponent(id)}`
      }
      if (u.pathname.startsWith('/shorts/')) {
        const id = u.pathname.split('/')[2]
        if (id) return `https://www.youtube.com/embed/${encodeURIComponent(id)}`
      }
    }
    if (host === 'vimeo.com') {
      const id = (u.pathname.match(/\/(\d+)/) || [])[1]
      if (id) return `https://player.vimeo.com/video/${encodeURIComponent(id)}`
    }
  } catch (_) { /* not a URL */ }
  return null
}

// Map a link to a slippy-map embed, or null when it is not a map link.
//
// Same discipline as the video embeds: this returns a URL on a host already in
// EMBED_HOST_RE, so a map link cannot become a route for arbitrary iframes.
// OpenStreetMap is preferred wherever a coordinate is available — it needs no
// API key and sets no advertising cookies. Google is accepted because it is
// what people actually paste, via the keyless `output=embed` form.
function toMapEmbedURL(href) {
  try {
    const u = new URL(href)
    const host = u.hostname.replace(/^www\./, '')

    if (host === 'openstreetmap.org' || host === 'osm.org') {
      // Share links carry the view in the fragment: #map=<zoom>/<lat>/<lon>
      const m = (u.hash || '').match(/map=(\d+(?:\.\d+)?)\/(-?\d+(?:\.\d+)?)\/(-?\d+(?:\.\d+)?)/)
      if (m) return osmEmbed(Number(m[2]), Number(m[3]), Number(m[1]))
      const lat = Number(u.searchParams.get('mlat'))
      const lon = Number(u.searchParams.get('mlon'))
      if (Number.isFinite(lat) && Number.isFinite(lon)) return osmEmbed(lat, lon, 15)
    }

    if (host === 'google.com' || host === 'maps.google.com' || host.endsWith('.google.com')) {
      if (!u.pathname.startsWith('/maps') && host !== 'maps.google.com') return null
      // A place URL carries its coordinate in the @lat,lng,zoom segment.
      const at = u.pathname.match(/@(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)/)
      if (at) return osmEmbed(Number(at[1]), Number(at[2]), 15)
      const q = u.searchParams.get('q')
      if (q) return `https://maps.google.com/maps?q=${encodeURIComponent(q)}&output=embed`
    }
  } catch (_) { /* not a URL */ }
  return null
}

// A small bounding box around a point, which is what OSM's embed takes. The
// span shrinks as zoom rises; the exact constant is cosmetic, not load-bearing.
function osmEmbed(lat, lon, zoom) {
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return null
  const span = Math.max(0.0015, 360 / Math.pow(2, Math.min(Math.max(zoom || 15, 1), 19)))
  const w = (lon - span).toFixed(5)
  const e = (lon + span).toFixed(5)
  const sLat = (lat - span / 2).toFixed(5)
  const n = (lat + span / 2).toFixed(5)
  return `https://www.openstreetmap.org/export/embed.html?bbox=${w},${sLat},${e},${n}` +
    `&layer=mapnik&marker=${lat.toFixed(5)},${lon.toFixed(5)}`
}

// Replace links that point at a playable video with an inline player. Runs on the
// parsed-but-unsanitized HTML; DOMPurify (below) then vets the result, so even
// the markup we inject is subject to the same sanitisation as everything else.
function embedVideos(html) {
  if (!html || (!html.includes('<a ') && !html.includes('http'))) return html
  let doc
  try {
    doc = new DOMParser().parseFromString(`<div>${html}</div>`, 'text/html')
  } catch (_) {
    return html
  }
  const root = doc.body.firstChild
  if (!root) return html
  root.querySelectorAll('a[href]').forEach((a) => {
    // Only convert "bare" links (link text equals the href) so we never eat a
    // link the author deliberately wrapped around words.
    const href = a.getAttribute('href') || ''
    const bare = (a.textContent || '').trim() === href.trim()
    if (!bare) return
    if (VIDEO_FILE_RE.test(href)) {
      const v = doc.createElement('video')
      v.setAttribute('controls', '')
      v.setAttribute('preload', 'metadata')
      v.setAttribute('src', href)
      v.className = 'md-video'
      a.replaceWith(v)
      return
    }
    if (PDF_FILE_RE.test(href) && isSameOrigin(href)) {
      const wrap = doc.createElement('div')
      wrap.className = 'md-pdf'
      const f = doc.createElement('iframe')
      f.setAttribute('src', href)
      f.setAttribute('loading', 'lazy')
      f.setAttribute('title', 'PDF preview')
      wrap.appendChild(f)
      a.replaceWith(wrap)
      return
    }
    if (AUDIO_FILE_RE.test(href)) {
      const au = doc.createElement('audio')
      au.setAttribute('controls', '')
      au.setAttribute('preload', 'metadata')
      au.setAttribute('src', href)
      au.className = 'md-audio'
      a.replaceWith(au)
      return
    }
    const mapEmbed = toMapEmbedURL(href)
    if (mapEmbed) {
      const wrap = doc.createElement('div')
      wrap.className = 'md-map'
      const f = doc.createElement('iframe')
      f.setAttribute('src', mapEmbed)
      f.setAttribute('loading', 'lazy')
      f.setAttribute('referrerpolicy', 'strict-origin-when-cross-origin')
      wrap.appendChild(f)
      a.replaceWith(wrap)
      return
    }
    const embed = toEmbedURL(href)
    if (embed) {
      const wrap = doc.createElement('div')
      wrap.className = 'md-embed'
      const f = doc.createElement('iframe')
      f.setAttribute('src', embed)
      f.setAttribute('allow', 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture')
      f.setAttribute('allowfullscreen', '')
      f.setAttribute('loading', 'lazy')
      f.setAttribute('referrerpolicy', 'strict-origin-when-cross-origin')
      wrap.appendChild(f)
      a.replaceWith(wrap)
    }
  })
  return root.innerHTML
}

/**
 * Parse markdown (with math) into sanitized HTML safe for `{@html}`.
 * @param {string} text
 * @returns {string} sanitized HTML
 */
export function parseMarkdown(text) {
  if (text == null || text === '') return ''
  let str = String(text).trim()
  if (str.startsWith('{') && str.endsWith('}')) {
    try {
      const parsed = JSON.parse(str)
      if (parsed && typeof parsed.response === 'string' && parsed.response.trim() !== '') {
        str = parsed.response.trim()
      } else if (parsed && typeof parsed.text === 'string' && parsed.text.trim() !== '') {
        str = parsed.text.trim()
      } else if (parsed && typeof parsed.message === 'string' && parsed.message.trim() !== '') {
        str = parsed.message.trim()
      }
    } catch (_) {}
  }
  let html
  try {
    html = marked.parse(str, { async: false })
    html = embedVideos(html)
  } catch (_) {
    // Never let a parse error break the bubble — fall back to escaped text.
    return DOMPurify.sanitize(str)
  }
  return DOMPurify.sanitize(html, {
    // audio/video and their `controls` attribute are already in DOMPurify's
    // defaults; they are listed for symmetry and so a future ALLOWED_TAGS
    // override cannot silently strip the players. Verified by mutation: removing
    // either changes nothing, so neither line is what makes playback work.
    ADD_TAGS: ['video', 'audio', 'source', 'iframe'],
    ADD_ATTR: ['target', 'rel', 'controls', 'preload', 'src', 'type', 'allow', 'allowfullscreen', 'loading', 'referrerpolicy', 'frameborder'],
  })
}

// ── Mermaid (initialized lazily, once) ───────────────────────────────────────
let mermaidReady = false
function ensureMermaid() {
  if (mermaidReady) return
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: 'dark',
    fontFamily: 'inherit',
    // Throw on bad diagrams instead of injecting Mermaid's raw "Syntax error in
    // text" graphic — we render our own clean fallback in the catch below.
    suppressErrorRendering: true,
  })
  mermaidReady = true
}

// Sanitize post-render SVG independently of the Markdown pass. In particular,
// forbid SVG's HTML escape hatches and active document elements while keeping
// the shapes, labels, markers, and styles Mermaid needs.
export function sanitizeRenderedSVG(svg) {
  return DOMPurify.sanitize(String(svg || ''), {
    USE_PROFILES: { svg: true, svgFilters: true },
    FORBID_TAGS: ['script', 'foreignObject', 'iframe', 'object', 'embed', 'link', 'meta'],
    FORBID_ATTR: ['onload', 'onerror', 'onclick', 'onbegin', 'onend'],
  })
}

let chartSeq = 0

// ── ECharts theming ──────────────────────────────────────────────────────────
// A premium, "ultra-modern" look inspired by TradingView (smooth area lines with
// a gradient fade + crosshair axis-pointer), Google Sheets (clean rounded bars,
// minimal axes) and ECharts/amCharts-grade polish. We restyle every series so
// charts look intentional regardless of what the agent passed — the data,
// labels, axes structure, and title are preserved; visuals are themed.

// Vibrant palette anchored on the app accent.
const CHART_PALETTE = ['#6c63ff', '#22d3ee', '#34d399', '#fbbf24', '#fb7185', '#a78bfa', '#38bdf8']

// Convert a #hex (3 or 6 digit) to an rgba() string with the given alpha.
function hexA(hex, a) {
  let h = String(hex || '').replace('#', '')
  if (h.length === 3) h = h.split('').map((c) => c + c).join('')
  if (h.length !== 6) return `rgba(108,99,255,${a})`
  const n = parseInt(h, 16)
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`
}

// Vertical ECharts gradient (top → bottom) for area/bar fills.
function vGrad(hex, topA, botA) {
  return new echarts.graphic.LinearGradient(0, 0, 0, 1, [
    { offset: 0, color: hexA(hex, topA) },
    { offset: 1, color: hexA(hex, botA) },
  ])
}

let _theme
function readTheme() {
  if (_theme) return _theme
  const root = getComputedStyle(document.documentElement)
  const v = (n, f) => root.getPropertyValue(n).trim() || f
  _theme = {
    accent: v('--accent', '#6c63ff'),
    text: v('--text', '#e6e9f2'),
    tick: v('--text-muted', '#8b93ab'),
    grid: 'rgba(255,255,255,0.06)',
    bg: v('--bg-elev', '#141927'),
    font: getComputedStyle(document.body).fontFamily || 'inherit',
  }
  return _theme
}

function styleAxis(option, key, t) {
  const apply = (ax) => {
    if (!ax || typeof ax !== 'object') return ax
    ax.axisLine = Object.assign({ show: false }, ax.axisLine)
    ax.axisTick = Object.assign({ show: false }, ax.axisTick)
    ax.axisLabel = Object.assign({ color: t.tick, fontSize: 11 }, ax.axisLabel)
    const isValue = ax.type === 'value' || key === 'yAxis'
    ax.splitLine = Object.assign({ show: isValue, lineStyle: { color: t.grid } }, ax.splitLine)
    return ax
  }
  if (Array.isArray(option[key])) option[key] = option[key].map(apply)
  else option[key] = apply(option[key] || {})
}

function decorateTooltip(tt, t) {
	// Agent-authored chart JSON must never reach ECharts' HTML tooltip path.
	// richText renders on canvas and formatter hooks are deliberately removed.
	delete tt.formatter
	tt.renderMode = 'richText'
  tt.backgroundColor = tt.backgroundColor || 'rgba(12,15,26,0.94)'
  tt.borderColor = tt.borderColor || hexA(t.accent, 0.45)
  tt.borderWidth = tt.borderWidth ?? 1
  tt.textStyle = Object.assign({ color: t.text }, tt.textStyle)
  tt.padding = tt.padding ?? 12
  delete tt.extraCssText
}

// Restyle a parsed ECharts option to the modern theme (in place) and return it.
// Exported so gui/src/lib/markdown.chartlayout.test.js can pin the title/legend
// lane invariant (Cohort G framework fix). The function is otherwise called
// only from mountEChart in this file.
export function themeEChartsOption(option) {
  if (!option || typeof option !== 'object') return option
  const t = readTheme()
  option.backgroundColor = 'transparent'
  // Authoritative palette: override whatever colors the agent passed so charts
  // look consistent and on-brand (the model's taste varies wildly).
  option.color = CHART_PALETTE
  option.textStyle = Object.assign({ color: t.tick, fontFamily: t.font }, option.textStyle)
  option.animationDuration = option.animationDuration ?? 800
  option.animationEasing = option.animationEasing || 'cubicOut'
  // Framework layout convention: title owns the top row, legend owns the
  // bottom row, plot owns the middle. Every previous overlap was traceable to
  // the title AND the legend both anchoring near `top:0` — the agent-generated
  // ECharts option would ship a long title AND a multi-entry legend and they
  // would collide because nothing reserved a lane. Anchoring the two on
  // opposite edges makes the collision structurally impossible regardless of
  // title length or number of series, and matches how mature dashboards
  // (Grafana / Metabase / Superset) lay charts out. Chart authors keep full
  // override power — every default here uses Object.assign(target=defaults,
  // agent's) so anything the caller sets wins.
  const hasTitle = !!option.title
  if (hasTitle) {
    option.title = Object.assign(
      { left: 'left', top: 8, textStyle: { color: t.text, fontSize: 15, fontWeight: 600 } },
      option.title,
    )
  }

  const series = Array.isArray(option.series) ? option.series : option.series ? [option.series] : []
  const hasAxis = series.some((s) => s && ['bar', 'line', 'scatter'].includes(s.type))
  const multi = series.length > 1
  const arcOrRadar = series.some((s) => s && (s.type === 'pie' || s.type === 'radar'))
  const hasLegend = multi || arcOrRadar

  // Reserved lanes: title top ~34px, legend bottom ~34px. Grid extends only
  // through the middle band. When either title or legend is absent, its lane
  // collapses back to a tight 8px so single-series charts aren't wasted.
  const gridTop = hasTitle ? 44 : 12
  const gridBottom = hasLegend ? 40 : 8

  if (hasAxis) {
    option.grid = Object.assign({ left: 8, right: 18, top: gridTop, bottom: gridBottom, containLabel: true }, option.grid)
    styleAxis(option, 'xAxis', t)
    styleAxis(option, 'yAxis', t)
    option.tooltip = Object.assign(
      { trigger: 'axis', axisPointer: { type: 'line', lineStyle: { color: hexA(t.accent, 0.35), type: 'dashed', width: 1 } } },
      option.tooltip,
    )
  } else {
    option.tooltip = Object.assign({ trigger: 'item' }, option.tooltip)
  }
  decorateTooltip(option.tooltip, t)

  if (hasLegend) {
    // Bottom-center legend on a horizontal row. `type: 'scroll'` lets long
    // legends paginate instead of wrapping into two rows that would eat into
    // grid.bottom — ECharts already reserved 40px for one row, so scrolling
    // is the honest way to handle overflow without touching the plot area.
    option.legend = Object.assign(
      {
        bottom: 8,
        left: 'center',
        orient: 'horizontal',
        type: 'scroll',
        itemWidth: 9,
        itemHeight: 9,
        itemGap: 14,
        textStyle: { color: t.text },
      },
      option.legend,
    )
    option.legend.icon = 'circle' // force circular markers regardless of agent input
  }

  // Our visual styling is authoritative — it OVERRIDES agent-supplied colors/
  // styles (Object.assign target = agent's object, our props applied on top) so
  // every chart gets the gradient/glow/rounded look. Data, labels, names, and
  // any label formatters the agent set are preserved.
  series.forEach((s, i) => {
    if (!s || typeof s !== 'object') return
    const color = CHART_PALETTE[i % CHART_PALETTE.length]
    if (s.type === 'line') {
      if (s.smooth === undefined) s.smooth = true
      s.symbol = 'circle'
      s.showSymbol = false
      s.lineStyle = Object.assign(s.lineStyle || {}, {
        width: 2.5, color, shadowColor: hexA(color, 0.5), shadowBlur: 12, shadowOffsetY: 6,
      })
      s.itemStyle = Object.assign(s.itemStyle || {}, { color, borderColor: '#fff', borderWidth: 2 })
      s.areaStyle = { color: vGrad(color, 0.35, 0.02) }
      s.emphasis = s.emphasis || { focus: 'series' }
    } else if (s.type === 'bar') {
      s.itemStyle = Object.assign(s.itemStyle || {}, { borderRadius: [6, 6, 0, 0], color: vGrad(color, 0.95, 0.35) })
      s.barMaxWidth = s.barMaxWidth || 46
      s.emphasis = { itemStyle: { color: vGrad(color, 1.0, 0.55) } }
    } else if (s.type === 'pie') {
      if (!s.radius) s.radius = '70%'
      s.itemStyle = Object.assign(s.itemStyle || {}, { borderRadius: 6, borderColor: t.bg, borderWidth: 3 })
      // Strip per-slice colors so the authoritative palette (option.color) wins.
      if (Array.isArray(s.data)) {
        s.data.forEach((d) => {
          if (d && typeof d === 'object' && d.itemStyle) delete d.itemStyle.color
        })
      }
      s.label = Object.assign(s.label || {}, { color: t.tick })
      s.emphasis = s.emphasis || { scale: true, scaleSize: 8, itemStyle: { shadowBlur: 16, shadowColor: 'rgba(0,0,0,0.4)' } }
    } else if (s.type === 'scatter') {
      s.itemStyle = Object.assign(s.itemStyle || {}, { color: hexA(color, 0.8), shadowBlur: 8, shadowColor: hexA(color, 0.4) })
      s.symbolSize = s.symbolSize || 12
    } else if (s.type === 'radar') {
      s.areaStyle = { color: hexA(color, 0.15) }
      s.lineStyle = Object.assign(s.lineStyle || {}, { color, width: 2 })
      s.itemStyle = Object.assign(s.itemStyle || {}, { color })
      s.symbolSize = s.symbolSize || 5
    }
  })

  if (option.radar) {
    option.radar = Object.assign(
      {
        axisName: { color: t.tick },
        splitLine: { lineStyle: { color: t.grid } },
        splitArea: { show: false },
        axisLine: { lineStyle: { color: t.grid } },
      },
      option.radar,
    )
  }
  return option
}

function showBlockError(pre, msg) {
  const note = document.createElement('div')
  note.className = 'mermaid-error' // reuse the error chip styling
  note.textContent = '⚠ ' + msg
  pre.replaceWith(note)
}

// Parse a Mermaid `xychart-beta` block into an ECharts option, so a model that
// insists on emitting Mermaid for data (some coder models do, even via a script)
// still gets a real themed chart instead of a fragile Mermaid render. Returns
// null when the block isn't a usable xychart.
//
// Grammar handled (Mermaid xychart-beta):
//   xychart-beta [horizontal]
//   title "…"
//   x-axis ["a","b",…]        (or a bare/numeric list)
//   y-axis "label" min --> max  (label/range ignored — ECharts autoscales)
//   line  [n, n, …]            (one or more series)
//   bar   [n, n, …]
function xychartToECharts(src) {
  const lines = String(src)
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  let title = ''
  let labels = []
  let horizontal = false
  const series = []
  for (const line of lines) {
    if (/^xychart/i.test(line)) {
      if (/\bhorizontal\b/i.test(line)) horizontal = true
      continue
    }
    let m
    if ((m = line.match(/^title\s+"?(.+?)"?\s*$/i))) {
      title = m[1]
    } else if ((m = line.match(/^x-axis\s+\[(.*)\]\s*$/i))) {
      labels = m[1]
        .split(',')
        .map((t) => t.trim().replace(/^["']|["']$/g, ''))
        .filter((t) => t !== '')
    } else if (/^y-axis\b/i.test(line)) {
      // axis label/range — ECharts handles scaling; ignore.
    } else if ((m = line.match(/^line\s+\[(.*)\]/i))) {
      series.push({ type: 'line', data: xyNums(m[1]) })
    } else if ((m = line.match(/^bar\s+\[(.*)\]/i))) {
      series.push({ type: 'bar', data: xyNums(m[1]) })
    }
  }
  if (!series.length || series.every((s) => s.data.length === 0)) return null

  const cat = { type: 'category', data: labels }
  const val = { type: 'value' }
  const option = {
    series: series.map((s) => ({ type: s.type, data: s.data })),
    // Mermaid "horizontal" swaps the axes.
    xAxis: horizontal ? val : cat,
    yAxis: horizontal ? cat : val,
  }
  if (title) option.title = { text: title }
  return option
}

function xyNums(s) {
  return s
    .split(',')
    .map((t) => parseFloat(t.trim()))
    .filter((n) => !Number.isNaN(n))
}

// addCodeCopyButton adds a hover "Copy" button to a fenced code block's <pre>,
// so users can grab code with one click (Modern Chat Workspace — rich rendering).
function addCodeCopyButton(codeEl) {
  const pre = codeEl.closest('pre')
  if (!pre || pre.dataset.copyBtn) return
  pre.dataset.copyBtn = '1'
  pre.style.position = pre.style.position || 'relative'
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'code-copy-btn'
  btn.textContent = 'Copy'
  btn.setAttribute('aria-label', 'Copy code')
  btn.addEventListener('click', async (e) => {
    e.preventDefault()
    e.stopPropagation()
    try {
      await navigator.clipboard.writeText(codeEl.textContent || '')
      btn.textContent = 'Copied!'
      setTimeout(() => { btn.textContent = 'Copy' }, 1200)
    } catch (_) { /* clipboard unavailable */ }
  })
  pre.appendChild(btn)
}

/**
 * Svelte action for a container holding parsed markdown. Pass the message text
 * as the parameter (`use:richRenderer={msg.text}`) so it re-runs when the bubble
 * content changes. Highlights code, renders mermaid/xychart fences to SVG, and
 * renders ```chart fences to interactive ECharts charts.
 */
export function richRenderer(node, value = '') {
  // ECharts instances (+ their ResizeObservers) to tear down on re-render/destroy.
  const liveCharts = []
  let lastValue = value == null ? '' : String(value)
  let rendered = false
  let runToken = 0

  function disposeCharts() {
    while (liveCharts.length) {
      const c = liveCharts.pop()
      try {
        c.ro && c.ro.disconnect()
      } catch (_) {
        /* ignore */
      }
      try {
        c.inst && c.inst.dispose()
      } catch (_) {
        /* already gone */
      }
    }
  }

  // Replace an element with a themed, responsive ECharts chart and track it for
  // cleanup. Shared by the ```chart path and the Mermaid-xychart conversion.
  function mountEChart(replaceEl, option) {
    const wrap = document.createElement('div')
    wrap.className = 'chart-canvas'
    const host = document.createElement('div')
    host.className = 'chart-echarts'
    wrap.appendChild(host)
    replaceEl.replaceWith(wrap)
    try {
      const inst = echarts.init(host, null, { renderer: 'canvas' })
      inst.setOption(themeEChartsOption(option))
      let ro
      if (typeof ResizeObserver !== 'undefined') {
        ro = new ResizeObserver(() => inst.resize())
        ro.observe(wrap)
      }
      liveCharts.push({ inst, ro })
    } catch (err) {
      showBlockError(wrap, 'Chart could not be rendered: ' + ((err && err.message) || 'invalid option'))
    }
  }

  // Build a table element from parsed rows. Every value goes in via textContent,
  // never innerHTML — the data is agent-authored and has already been through
  // DOMPurify once as text; re-injecting it as markup would hand it a second
  // chance to be interpreted.
  function buildTable({ columns, rows, truncated }) {
    const wrap = document.createElement('div')
    wrap.className = 'md-table-wrap'
    const table = document.createElement('table')
    table.className = 'md-data-table'

    const thead = document.createElement('thead')
    const hr = document.createElement('tr')
    columns.forEach((c) => {
      const th = document.createElement('th')
      th.textContent = c
      hr.appendChild(th)
    })
    thead.appendChild(hr)
    table.appendChild(thead)

    const tbody = document.createElement('tbody')
    rows.forEach((r) => {
      const tr = document.createElement('tr')
      r.forEach((cell) => {
        const td = document.createElement('td')
        td.textContent = cell
        // Right-align numbers so a column of figures lines up on the decimal.
        if (cell !== '' && !Number.isNaN(Number(cell))) td.className = 'num'
        tr.appendChild(td)
      })
      tbody.appendChild(tr)
    })
    table.appendChild(tbody)
    wrap.appendChild(table)

    if (truncated) {
      const note = document.createElement('div')
      note.className = 'md-table-note'
      note.textContent = `Showing the first ${rows.length} rows.`
      wrap.appendChild(note)
    }
    return wrap
  }


  // Group consecutive images into a grid and open any image full-size on click.
  //
  // Only images that are alone in their paragraph are grouped: an image the
  // author placed inline with text belongs where they put it.
  function layoutImages(root) {
    const paras = Array.from(root.querySelectorAll('p'))
    let run = []
    const flush = () => {
      if (run.length > 1) {
        const grid = document.createElement('div')
        grid.className = 'md-gallery'
        run[0].before(grid)
        run.forEach((p) => grid.appendChild(p.querySelector('img')))
        run.forEach((p) => p.remove())
      }
      run = []
    }
    for (const p of paras) {
      const imgs = p.querySelectorAll('img')
      const loneImage = imgs.length === 1 && (p.textContent || '').trim() === ''
      if (loneImage) run.push(p)
      else flush()
    }
    flush()

    root.querySelectorAll('img').forEach((img) => {
      if (img.dataset.zoomable) return
      img.dataset.zoomable = '1'
      img.classList.add('md-zoomable')
      img.addEventListener('click', () => openLightbox(img.getAttribute('src'), img.getAttribute('alt') || ''))
    })
  }

  // A single overlay reused for every image, removed on click or Escape. Kept
  // deliberately plain: the job is "see it bigger", not a carousel.
  function openLightbox(src, alt) {
    if (!src) return
    const overlay = document.createElement('div')
    overlay.className = 'md-lightbox'
    overlay.setAttribute('role', 'dialog')
    overlay.setAttribute('aria-label', alt || 'Image')
    const big = document.createElement('img')
    big.setAttribute('src', src)
    if (alt) big.setAttribute('alt', alt)
    overlay.appendChild(big)

    const close = () => {
      overlay.remove()
      document.removeEventListener('keydown', onKey)
    }
    const onKey = (e) => { if (e.key === 'Escape') close() }
    overlay.addEventListener('click', close)
    document.addEventListener('keydown', onKey)
    document.body.appendChild(overlay)
  }

  function run() {
    disposeCharts()

    // (1) Syntax-highlight fenced code — skip the blocks we render specially.
    node.querySelectorAll('pre code').forEach((el) => {
      const cls = el.className || ''
      if (
        cls.includes('language-mermaid') ||
        cls.includes('language-xychart') ||
        cls.includes('language-chart') ||
        cls.includes('language-csv') ||
        cls.includes('language-tsv') ||
        cls.includes('language-data') ||
        el.dataset.hl
      )
        return
      try {
        hljs.highlightElement(el)
      } catch (_) {
        /* leave the code unhighlighted on failure */
      }
      el.dataset.hl = '1'
      addCodeCopyButton(el)
    })

    // (2) Replace mermaid / xychart fences with rendered SVG diagrams.
    const diagrams = node.querySelectorAll('code.language-mermaid, code.language-xychart')
    if (diagrams.length) {
      ensureMermaid()
      diagrams.forEach(async (codeEl) => {
        const pre = codeEl.closest('pre') || codeEl
        if (pre.dataset.done) return
        pre.dataset.done = '1'
        const src = (codeEl.textContent || '').trim()
        if (!src) return
        // Mermaid is for DIAGRAMS, not data plots. Models sometimes emit an
        // `xychart` to chart data — which is the generate_chart tool's job and
        // also frequently fails to parse. Don't even try: show an on-message note
        // so the user never sees a raw Mermaid error and the intent is clear.
        const isDataChart = (codeEl.className || '').includes('language-xychart') || /^xychart/i.test(src)
        if (isDataChart) {
          // Don't hand data plots to Mermaid — convert to a real ECharts chart so
          // the user always gets a themed chart, even when the model emits Mermaid.
          const option = xychartToECharts(src)
          if (option) {
            mountEChart(pre, option)
          } else {
            showBlockError(pre, 'Data charts are rendered with the generate_chart tool, not Mermaid — ask again and the agent will draw it.')
          }
          return
        }
        try {
          const { svg } = await mermaid.render('sm-chart-' + ++chartSeq, src)
          const wrap = document.createElement('div')
          wrap.className = 'mermaid-chart'
          wrap.innerHTML = sanitizeRenderedSVG(svg)
          pre.replaceWith(wrap)
        } catch (err) {
          showBlockError(pre, 'Diagram could not be rendered: ' + ((err && err.message) || 'invalid mermaid'))
        }
      })
    }

    // (5) Images: group a run of them into a gallery and make each one zoomable.
    //
    // Several images in a row used to stack vertically at full width, so three
    // charts meant three screens of scrolling, and a dense one could not be read
    // at bubble width with no way to enlarge it.
    layoutImages(node)

    // (4) Render ```csv / ```tsv / ```data fences as real tables.
    //
    // A dataset an agent emitted used to arrive as highlighted source: the data
    // was all there, but you could not scan a column or compare two rows, which
    // is the entire reason it was put in a table shape to begin with.
    node.querySelectorAll('code.language-csv, code.language-tsv, code.language-data').forEach((codeEl) => {
      const pre = codeEl.closest('pre') || codeEl
      if (pre.dataset.done) return
      pre.dataset.done = '1'
      const lang = (codeEl.className || '').replace(/^.*language-/, '').split(/\s/)[0]
      const table = tableFromFence(lang, codeEl.textContent || '')
      if (!table) {
        // Leave the source visible rather than replacing it with an error: the
        // text is still the data, and the reader can act on it.
        delete pre.dataset.done
        return
      }
      pre.replaceWith(buildTable(table))
    })

    // (3) Render ```chart JSON fences as interactive ECharts charts.
    node.querySelectorAll('code.language-chart').forEach((codeEl) => {
      const pre = codeEl.closest('pre') || codeEl
      if (pre.dataset.done) return
      pre.dataset.done = '1'
      const raw = (codeEl.textContent || '').trim()
      if (!raw) return
      let option
      try {
        option = JSON.parse(raw) // JSON only — no functions/eval can ride in
      } catch (err) {
        showBlockError(pre, 'Invalid chart JSON: ' + ((err && err.message) || 'parse error'))
        return
      }
      const series = option && option.series
      if (!series || (Array.isArray(series) && series.length === 0)) {
        showBlockError(pre, 'Chart needs a "series".')
        return
      }
      mountEChart(pre, option)
    })
  }

  // Defer to a microtask so Svelte has injected the {@html} content first, and
  // a long message list doesn't block the main thread synchronously on mount.
  // The action replaces fenced chart blocks with live DOM. Svelte can still call
  // update() later for unrelated chat-store changes; if the message text did not
  // change, do NOT dispose the chart, because the original code fence is gone.
  function scheduleRun() {
    const token = ++runToken
    Promise.resolve().then(() => {
      if (token !== runToken) return
      run()
      rendered = true
    })
  }

  scheduleRun()
  return {
    update(nextValue = '') {
      const normalized = nextValue == null ? '' : String(nextValue)
      if (rendered && normalized === lastValue) return
      lastValue = normalized
      scheduleRun()
    },
    destroy() {
      runToken++
      disposeCharts()
    },
  }
}
