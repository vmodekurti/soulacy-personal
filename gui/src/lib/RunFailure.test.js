// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { tick } from 'svelte'
import RunFailure from './RunFailure.svelte'

it('shows the exact error and action, with checks available separately', async () => {
  const target = document.createElement('div')
  document.body.appendChild(target)
  const detail = 'model missing: ' + 'long model identifier '.repeat(30) + '<script>unsafe()</script>'
  const component = new RunFailure({ target, props: { failure: {
    summary: 'A configured model was not found.', step: 'open_notebook_generate_podcast',
    detail, nextAction: 'Select an available model in Open Notebook.',
    checks: ['Return a playable audio link'],
  } } })
  try {
    await tick()
    expect(target.textContent).toContain(detail)
    expect(target.textContent).toContain('What to do: Select an available model in Open Notebook.')
    expect(target.querySelector('script')).toBeNull()
    expect(target.querySelector('details').open).toBe(false)
  } finally { component.$destroy(); target.remove() }
})
