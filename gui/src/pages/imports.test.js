import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

// A page that USES a module singleton must IMPORT it.
//
// Studio.svelte called api.studio.diff() and api.studio.addStep() in three
// places and never imported `api`. Svelte compiles an undeclared identifier as
// a global reference, so the build was clean and the type of failure was a
// runtime ReferenceError — inside a try/catch, which turned it into
// "could not preview the change". A message about the change, not about the
// missing import. The repair-preview feature was dead and read as flaky.
//
// Nothing in the toolchain catches this: there is no linter in the build, and a
// unit test only catches it if it happens to walk the one branch that throws.
// A static check does, for every page, in milliseconds.

const here = dirname(fileURLToPath(import.meta.url))

// The singletons worth policing: module-level objects that pages dereference by
// name. A missing import for one of these is always a bug, never a global.
const SINGLETONS = ['api', 'apiFetch', 'bridge']

function pageFiles() {
  return readdirSync(here)
    .filter((f) => f.endsWith('.svelte'))
    .map((f) => ({ name: f, src: readFileSync(join(here, f), 'utf8') }))
}

// scriptOf returns the component's <script> body with comments stripped — the
// only place an import can live, and the only place we should judge usage from.
//
// Stripping comments is not cosmetic. These files explain themselves at length,
// and PluginFrame.svelte mentions "postMessage RPC bridge" in prose; without
// this the check reported a missing import for a word in a sentence. A rule
// that cries wolf on comments gets switched off, which costs more than it saves.
function scriptOf(src) {
  const m = src.match(/<script[^>]*>([\s\S]*?)<\/script>/)
  if (!m) return ''
  return m[1]
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

function importsName(script, name) {
  // `import { api } from '...'`, `import { api, apiFetch } from '...'`,
  // `import api from '...'`, or an alias binding `as api`.
  const named = new RegExp(`import\\s*\\{[^}]*\\b${name}\\b[^}]*\\}\\s*from`, 's')
  const def = new RegExp(`import\\s+${name}\\s+from`)
  const aliased = new RegExp(`as\\s+${name}\\b`)
  return named.test(script) || def.test(script) || aliased.test(script)
}

function declaresName(script, name) {
  return new RegExp(`(?:const|let|var|function)\\s+${name}\\b`).test(script)
}

// usesName looks for the identifier being dereferenced or called, and ignores
// property positions (`.api`) and string/key occurrences (`api:`).
function usesName(script, name) {
  return new RegExp(`(?<![.\\w$])${name}\\s*(?:\\.|\\()`).test(script)
}

describe('pages import the singletons they use', () => {
  const pages = pageFiles()

  it('finds page components to check', () => {
    expect(pages.length).toBeGreaterThan(0)
  })

  for (const { name, src } of pages) {
    const script = scriptOf(src)
    for (const singleton of SINGLETONS) {
      if (!usesName(script, singleton)) continue
      it(`${name} imports ${singleton}`, () => {
        const ok = importsName(script, singleton) || declaresName(script, singleton)
        expect(
          ok,
          `${name} dereferences \`${singleton}\` but never imports or declares it — ` +
            `Svelte compiles that to a global read, so it builds fine and throws ReferenceError at run time`,
        ).toBe(true)
      })
    }
  }
})
