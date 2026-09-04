<script>
  /*
   * YamlView — a zero-dependency, syntax-highlighted YAML editor.
   *
   * Technique: a transparent <textarea> sits on top of a highlighted <pre>
   * that mirrors the same text, with a line-number gutter on the left. The
   * textarea owns input + the caret; the <pre> shows the colors; their scroll
   * positions are kept in sync. No external editor library, so it bundles with
   * the existing Vite build and themes off the app's CSS variables.
   *
   * Tokens highlighted: comments, keys, list markers, strings, numbers,
   * booleans/null, and — called out specially because they're the source of a
   * whole class of bugs — Go-template expressions like {{ .notebook.id }}.
   */
  export let value = ''
  export let readonly = false

  let taEl, preEl, gutterEl

  // ── Highlighting ──────────────────────────────────────────────────────────
  function escapeHtml(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  }

  // Single-pass inline tokenizer over an ALREADY HTML-escaped string. Using one
  // regex + replacer means inserted <span> markup is never re-scanned, so we
  // can't corrupt our own tags.
  const INLINE =
    /(\{\{[^}]*\}\})|("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|\b(true|false|null|yes|no|on|off)\b|(-?\b\d+(?:\.\d+)?\b)/g
  function highlightInline(escaped) {
    // Guard against pathological lines (huge inline code/base64): skip the regex
    // and return the plain escaped text so a single line can never stall or
    // break the whole highlight pass.
    if (escaped.length > 1000) return escaped
    return escaped.replace(INLINE, (m, tmpl, str, bool, num) => {
      if (tmpl) return `<span class="y-tmpl">${m}</span>`
      if (str) return `<span class="y-str">${m}</span>`
      if (bool) return `<span class="y-bool">${m}</span>`
      if (num) return `<span class="y-num">${m}</span>`
      return m
    })
  }

  function highlightLine(line) {
    // Full-line comment.
    if (/^\s*#/.test(line)) return `<span class="y-comment">${escapeHtml(line)}</span>`

    // key:  (optionally preceded by a "- " list marker), then the value tail.
    let m = line.match(/^(\s*)(-\s+)?([^:#\s][^:#]*?):(\s.*|)$/)
    if (m) {
      const [, indent, dash, key, rest] = m
      const eDash = dash ? `<span class="y-dash">${escapeHtml(dash)}</span>` : ''
      return (
        escapeHtml(indent) +
        eDash +
        `<span class="y-key">${escapeHtml(key)}</span>` +
        `<span class="y-colon">:</span>` +
        highlightInline(escapeHtml(rest))
      )
    }

    // "- value" list item with no key.
    m = line.match(/^(\s*)(-\s+)(.*)$/)
    if (m) {
      const [, indent, dash, val] = m
      return (
        escapeHtml(indent) +
        `<span class="y-dash">${escapeHtml(dash)}</span>` +
        highlightInline(escapeHtml(val))
      )
    }

    // Plain scalar / block-scalar continuation.
    return highlightInline(escapeHtml(line))
  }

  // Never let one bad line break the whole highlight pass — fall back to plain
  // escaped text for that line so the editor always shows its content.
  function safeHighlightLine(line) {
    try {
      return highlightLine(line)
    } catch (_) {
      return escapeHtml(line)
    }
  }

  $: lines = (value == null ? '' : String(value)).split('\n')
  $: highlighted = lines.map(safeHighlightLine).join('\n')
  $: lineCount = lines.length

  // ── Scroll + key handling ───────────────────────────────────────────────
  function syncScroll() {
    if (!taEl) return
    if (preEl) {
      preEl.scrollTop = taEl.scrollTop
      preEl.scrollLeft = taEl.scrollLeft
    }
    if (gutterEl) gutterEl.scrollTop = taEl.scrollTop
  }

  function onKeydown(e) {
    if (e.key === 'Tab' && !readonly) {
      e.preventDefault()
      const el = e.target
      const s = el.selectionStart
      const en = el.selectionEnd
      value = value.slice(0, s) + '  ' + value.slice(en)
      // Restore caret after the inserted two spaces (next tick).
      requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = s + 2 })
    }
  }
</script>

<div class="yv">
  <div class="yv-gutter" bind:this={gutterEl} aria-hidden="true">
    {#each Array(lineCount) as _, i}
      <div class="yv-ln">{i + 1}</div>
    {/each}
  </div>
  <div class="yv-main">
    <pre class="yv-pre" bind:this={preEl} aria-hidden="true"><code>{@html highlighted + '\n'}</code></pre>
    <textarea
      class="yv-ta"
      bind:this={taEl}
      bind:value
      {readonly}
      spellcheck="false"
      autocomplete="off"
      autocapitalize="off"
      autocorrect="off"
      wrap="off"
      on:scroll={syncScroll}
      on:keydown={onKeydown}
    ></textarea>
  </div>
</div>

<style>
  .yv {
    display: flex;
    /* Fill the flex parent via flex-grow + stretch instead of height:100% —
       a percentage height collapses to 0 inside an indefinite-height flex
       wrapper (the Validate panel wrapper), which blanked the editor. A
       concrete min-height guarantees it's never invisible even if the parent
       chain provides no height at all. */
    flex: 1 1 auto;
    align-self: stretch;
    min-height: 360px;
    height: 100%;
    background: #0e1020;
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
    font-family: ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", monospace;
    font-size: 13px;
    line-height: 20px;
  }

  /* Gutter */
  .yv-gutter {
    flex: 0 0 auto;
    min-width: 44px;
    padding: 12px 8px 12px 0;
    text-align: right;
    color: #4a5078;
    background: #0b0d1a;
    border-right: 1px solid var(--border);
    overflow: hidden;
    user-select: none;
  }
  .yv-ln { height: 20px; padding-right: 6px; }

  /* Editor body: pre + textarea overlapping pixel-for-pixel */
  .yv-main { position: relative; flex: 1 1 auto; min-width: 0; min-height: 0; }
  .yv-pre, .yv-ta {
    margin: 0;
    padding: 12px 14px;
    border: 0;
    /* Pin IDENTICAL text metrics on both layers with !important. Relying on
       `font: inherit` let the app's global `:global(textarea)` rule (font-size
       14px) leak into the textarea while the <pre> stayed 13px, so the caret's
       line box and the highlighted text drifted apart — the caret landed on the
       previous/next line. Explicit, equal values keep them pixel-locked. */
    font-family: ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", monospace !important;
    font-size: 13px !important;
    line-height: 20px !important;
    letter-spacing: 0 !important;
    word-spacing: 0 !important;
    font-variant-ligatures: none;
    tab-size: 2;
    white-space: pre;
    overflow: auto;
    box-sizing: border-box;
    position: absolute;
    inset: 0;
  }
  .yv-pre {
    color: #d7dcf5 !important;
    -webkit-text-fill-color: #d7dcf5;
    pointer-events: none;
    overflow: hidden;            /* scroll is driven by the textarea */
    z-index: 3;
  }
  /* Defend against any global `pre`/`code` rule hiding the highlight text. */
  .yv-pre code {
    font: inherit;
    color: inherit !important;
    -webkit-text-fill-color: inherit;
    display: inline;
    background: none;
  }
  .yv-ta {
    color: transparent !important;
    -webkit-text-fill-color: transparent;
    background: transparent !important;
    background-color: transparent !important;
    /* Reset the native field rendering — without this, macOS/WebKit paints an
       opaque textarea surface OVER the highlight layer, hiding the code even
       though `background-color` computes transparent. This is the fix for the
       "content present but blank" symptom. */
    -webkit-appearance: none;
    appearance: none;
    border: 0;
    caret-color: #8ab4ff;
    resize: none;
    outline: none;
    z-index: 2;
  }
  .yv-ta::selection { background: rgba(108,99,255,.35); }

  /* Token colors */
  .yv :global(.y-key)     { color: #7fb4ff; }
  .yv :global(.y-colon)   { color: #6b7294; }
  .yv :global(.y-dash)    { color: #6b7294; }
  .yv :global(.y-str)     { color: #8ed09a; }
  .yv :global(.y-num)     { color: #e7b765; }
  .yv :global(.y-bool)    { color: #c792ea; }
  .yv :global(.y-comment) { color: #5a6080; font-style: italic; }
  /* Template expressions are tinted so {{ .notebook.id }} stands out. NO padding
     or border — those widen the highlight layer's glyphs relative to the plain
     textarea and push the caret sideways off the text. Background + radius are
     paint-only, so they don't shift layout. */
  .yv :global(.y-tmpl) {
    color: #ffd479;
    background: rgba(255,212,121,.14);
    border-radius: 3px;
  }
</style>
