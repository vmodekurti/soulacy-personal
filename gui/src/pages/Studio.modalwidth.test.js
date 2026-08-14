// A width override that can never win is worse than no override.
//
// Studio's modals share `.modal { width: min(460px, 92vw) }`, declared near the
// bottom of the stylesheet. A dialog that needs more room adds its own rule.
// Two of them qualify with `.modal` and work:
//
//   .modal.model-modal   { width: min(760px, 94vw); }
//   .modal.yaml-browser  { width: min(920px, 94vw); }
//
// "My Workflows" wrote `.library-modal { width: min(860px, 94vw); }` — one
// class, same specificity as `.modal`, declared EARLIER in the file. CSS
// resolves that tie by source order, so `.modal` won and the panel silently
// stayed at 460px. Nothing failed; it just rendered narrow.
//
// The consequence was not cosmetic. At 460px the six per-item action buttons
// took the row, `.picker-main` collapsed, and `overflow-wrap: anywhere` broke
// the workflow name at every character — "Stock Advisor" came out as a vertical
// column of single letters, which is how the bug was finally spotted.
//
// This reads the component's stylesheet and fails on any modal width override
// that is written so it cannot take effect. It catches the next one too, which
// a screenshot of this one would not.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('./Studio.svelte', import.meta.url)), 'utf8')

/** The component stylesheet, so component markup cannot be mistaken for CSS.
 * Studio imports its large stylesheet as a module: Svelte does not reliably
 * bundle `<style src="...">`, which is precisely how the whole page once fell
 * back to browser-default styling. Keep supporting an inline/external style tag
 * for smaller components, but prefer the explicit CSS import used in production.
 */
function stylesheet() {
  const imported = src.match(/import\s+["'](\.\/Studio\.css)["']/)
  if (imported) {
    return readFileSync(fileURLToPath(new URL(imported[1], import.meta.url)), 'utf8')
  }
  const i = src.lastIndexOf('<style')
  const start = src.indexOf('>', i) + 1
  const end = src.lastIndexOf('</style>')
  const tag = src.slice(i, start)
  const external = tag.match(/\bsrc=["']([^"']+)["']/)
  if (external) {
    return readFileSync(fileURLToPath(new URL(external[1], import.meta.url)), 'utf8')
  }
  return src.slice(start, end)
}

/** Index of the base `.modal { … }` rule that every dialog inherits. */
function baseModalRuleIndex(css) {
  const m = css.match(/(^|\n)\s*\.modal\s*\{/)
  return m ? m.index : -1
}

describe('modal width overrides', () => {
  it('has a base .modal width to override', () => {
    const css = stylesheet()
    const i = baseModalRuleIndex(css)
    expect(i, 'no base `.modal {` rule found — this guard has stopped guarding anything').toBeGreaterThan(-1)
    expect(css.slice(i, i + 200)).toMatch(/(^|[;{\s])width\s*:/)
  })

  it('every modal width override can actually beat the base rule', () => {
    const css = stylesheet()
    const baseAt = baseModalRuleIndex(css)

    // Any rule whose selector mentions a *-modal / browser-ish dialog class and
    // that sets a width.
    const ruleRe = /(^|\n)([^{}\n][^{}]*)\{([^}]*)\}/g
    const problems = []
    let m
    while ((m = ruleRe.exec(css)) !== null) {
      const selector = m[2].trim()
      const body = m[3]
      const at = m.index
      if (!/(^|[;{\s])width\s*:/.test(body)) continue
      // Only selectors that target a specific dialog.
      if (!/\.[a-z0-9-]*(modal|browser|panel|dialog)\b/i.test(selector)) continue
      if (/^\.modal\s*$/.test(selector)) continue // the base rule itself

      const qualified = /\.modal\./.test(selector) || /\.modal\b[^,]*\./.test(selector)
      const declaredAfterBase = at > baseAt
      // It wins if it is more specific than `.modal`, or (same specificity) it
      // comes later in the file.
      if (!qualified && !declaredAfterBase) {
        problems.push(selector)
      }
    }

    expect(
      problems,
      'these rules set a modal width but are declared before `.modal` with no ' +
      'extra specificity, so `.modal` overrides them and the dialog silently ' +
      'renders at the default width. Qualify them as `.modal.<name>` (the way ' +
      '.modal.model-modal and .modal.yaml-browser do):\n  ' + problems.join('\n  '),
    ).toEqual([])
  })

  it('gives the library modal the width it asks for', () => {
    expect(stylesheet()).toMatch(/\.modal\.library-modal\s*\{[^}]*width:\s*min\(860px/)
  })

  // min-width: 0 on a flex child lets it be crushed to nothing. Combined with a
  // character-level wrap that is what stacked the name vertically, so the floor
  // is part of the fix rather than incidental tidying.
  it('never lets the workflow name column be crushed to nothing', () => {
    const css = stylesheet()
    const rule = css.match(/\.lib-item\s+\.picker-main\s*\{([^}]*)\}/)
    expect(rule, '.lib-item .picker-main rule missing').toBeTruthy()
    expect(rule[1]).not.toMatch(/min-width:\s*0/)
    expect(rule[1]).toMatch(/min-width:\s*\d{2,}px/)
  })

  it('does not break workflow names mid-word', () => {
    const css = stylesheet()
    const rule = css.match(/\.lib-item\s+\.picker-name[^{]*\{([^}]*)\}/)
    expect(rule, 'the library name wrap rule went missing').toBeTruthy()
    expect(
      rule[1],
      '`overflow-wrap: anywhere` breaks between characters when the column is ' +
      'narrow, which is what rendered the name as a vertical column of letters',
    ).not.toMatch(/anywhere/)
  })
})
