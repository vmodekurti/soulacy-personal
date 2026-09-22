// @vitest-environment jsdom
// Each presentation kind (#199) must reach the screen as its own element, not
// as text, and compact mode must hold back the tail.
import { describe, it, expect, afterEach, vi } from 'vitest'

let cmp = null, target = null
afterEach(() => { if (cmp) cmp.$destroy(); if (target) target.remove(); cmp = null; target = null })

const blocks = [
  { kind: 'metrics', items: [{ label: 'Best fare', value: '$839', hint: 'AA nonstop' }, { label: 'Options', value: '4' }] },
  { kind: 'comparison', title: 'Top options', columns: ['Airline', 'Stops', 'Price'], numeric: ['Price'], best: { column: 'Price', row: 1 },
    rows: [['British Airways', '1', '$770'], ['American Airlines', '0', '$839']] },
  { kind: 'timeline', title: 'Today', items: [{ at: '09:30', title: 'Standup' }, { at: '15:00', title: 'Dentist', detail: 'leave by 14:30' }] },
  { kind: 'links', items: [{ title: 'Momondo', url: 'https://www.momondo.com/x' }] },
  { kind: 'checklist', items: [{ label: 'postgres dump', done: true }, { label: 'qdrant snapshot', done: false }] },
  { kind: 'markdown', text: 'Plain **prose** at the end.' },
]

async function mount(props) {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const { default: P } = await import('./Presentation.svelte')
  target = document.createElement('div'); document.body.appendChild(target)
  cmp = new P({ target, props }); await new Promise(r => setTimeout(r, 20))
}

describe('Presentation', () => {
  it('renders every kind as its own element', async () => {
    await mount({ blocks })
    expect(target.querySelectorAll('.metric').length).toBe(2)
    expect(target.querySelector('.metric .v').textContent).toBe('$839')
    expect(target.querySelectorAll('table.cmp tbody tr').length).toBe(2)
    expect(target.querySelector('table.cmp tr.best td').textContent).toContain('American Airlines')
    expect(target.querySelector('table.cmp td.num').textContent).toContain('$770')
    expect([...target.querySelectorAll('.timeline time')].map(t => t.textContent)).toEqual(['09:30', '15:00'])
    expect(target.querySelector('.link .lh').textContent).toContain('momondo.com')
    expect(target.querySelectorAll('.checks li.done').length).toBe(1)
    expect(target.querySelector('.md strong').textContent).toBe('prose')
  })

  it('compact shows the first two blocks and says how many more', async () => {
    await mount({ blocks, compact: true })
    expect(target.querySelectorAll('.metric').length).toBe(2)
    expect(target.querySelector('table.cmp')).toBeTruthy()
    expect(target.querySelector('.timeline')).toBeNull()
    expect(target.querySelector('.more-hint').textContent).toContain('+4 more')
  })

  it('compact prefers typed blocks over prose', async () => {
    await mount({ blocks: [{ kind: 'markdown', text: 'intro' }, blocks[1], blocks[2], { kind: 'markdown', text: 'outro' }], compact: true })
    expect(target.querySelector('.md')).toBeNull()
    expect(target.querySelector('table.cmp')).toBeTruthy()
    expect(target.querySelector('.timeline')).toBeTruthy()
  })

  it('a compact grid keeps the first text column and the numbers', async () => {
    const wide = { kind: 'comparison', columns: ['#', 'Airline', 'Outbound', 'Return', 'Stops', 'Price'], numeric: ['Price'], best: { column: 'Price', row: 0 },
      rows: [['1', 'BA', '4:29pm', '4:44pm', '1', '$770'], ['2', 'AA', '5:55pm', '1:10pm', '0', '$839']] }
    await mount({ blocks: [wide], compact: true })
    expect([...target.querySelectorAll('table.cmp th')].map(t => t.textContent)).toEqual(['Airline', 'Price'])
    expect(target.querySelector('table.cmp tr.best td.num').textContent).toContain('$770')
  })
})
