// Parsing a dataset an agent emitted. The fiddly parts — quoted fields with
// commas, embedded newlines, escaped quotes — are exactly the ones that show up
// in real data (company names, addresses, free text) and exactly the ones a
// naive split on commas destroys.

import { describe, it, expect } from 'vitest'
import { parseDelimited, parseJSONTable, tableFromFence, MAX_TABLE_ROWS } from './datatable.js'

describe('parseDelimited', () => {
  it('reads a header and rows', () => {
    const t = parseDelimited('ticker,price\nMU,861.0\nSTX,800.99')
    expect(t.columns).toEqual(['ticker', 'price'])
    expect(t.rows).toEqual([['MU', '861.0'], ['STX', '800.99']])
  })

  it('keeps a comma that lives inside a quoted field', () => {
    const t = parseDelimited('name,sector\n"Advanced Micro Devices, Inc.",Semiconductors')
    expect(t.rows[0]).toEqual(['Advanced Micro Devices, Inc.', 'Semiconductors'])
  })

  it('keeps a newline inside a quoted field', () => {
    const t = parseDelimited('name,note\nMU,"line one\nline two"')
    expect(t.rows).toHaveLength(1)
    expect(t.rows[0][1]).toBe('line one\nline two')
  })

  it('reads "" as one literal quote', () => {
    const t = parseDelimited('sym,desc\nX,"a ""quoted"" word"')
    expect(t.rows[0][1]).toBe('a "quoted" word')
  })

  it('detects tab-separated data from the header', () => {
    const t = parseDelimited('a\tb\tc\n1\t2\t3')
    expect(t.columns).toEqual(['a', 'b', 'c'])
    expect(t.rows[0]).toEqual(['1', '2', '3'])
  })

  it('pads a short row rather than shifting later columns left', () => {
    const t = parseDelimited('a,b,c\n1,2')
    expect(t.rows[0]).toEqual(['1', '2', ''])
  })

  it('handles CRLF and a trailing newline', () => {
    const t = parseDelimited('a,b\r\n1,2\r\n')
    expect(t.rows).toEqual([['1', '2']])
  })

  it('returns nothing for empty input', () => {
    expect(parseDelimited('   ').columns).toEqual([])
  })

  it('caps very large datasets and says so', () => {
    const rows = Array.from({ length: MAX_TABLE_ROWS + 50 }, (_, i) => `r${i},1`).join('\n')
    const t = parseDelimited('name,v\n' + rows)
    expect(t.rows).toHaveLength(MAX_TABLE_ROWS)
    expect(t.truncated).toBe(true)
  })
})

describe('parseJSONTable', () => {
  it('reads an array of objects', () => {
    const t = parseJSONTable('[{"ticker":"MU","price":861},{"ticker":"STX","price":800.99}]')
    expect(t.columns).toEqual(['ticker', 'price'])
    expect(t.rows).toEqual([['MU', '861'], ['STX', '800.99']])
  })

  // A row missing a key must leave a hole, not slide the next value into its
  // column — the failure that makes a table quietly wrong rather than obviously
  // broken.
  it('keeps the union of keys and leaves gaps empty', () => {
    const t = parseJSONTable('[{"a":1},{"b":2}]')
    expect(t.columns).toEqual(['a', 'b'])
    expect(t.rows).toEqual([['1', ''], ['', '2']])
  })

  it('reads an explicit columns/rows shape', () => {
    const t = parseJSONTable('{"columns":["x","y"],"rows":[[1,2],[3,4]]}')
    expect(t.columns).toEqual(['x', 'y'])
    expect(t.rows).toEqual([['1', '2'], ['3', '4']])
  })

  it('shows a nested value as JSON rather than [object Object]', () => {
    const t = parseJSONTable('[{"sym":"MU","flags":{"ok":true}}]')
    expect(t.rows[0][1]).toBe('{"ok":true}')
  })

  it('declines things that are not tabular', () => {
    expect(parseJSONTable('{"just":"an object"}')).toBeNull()
    expect(parseJSONTable('[1,2,3]')).toBeNull()
    expect(parseJSONTable('[]')).toBeNull()
    expect(parseJSONTable('not json')).toBeNull()
  })
})

describe('tableFromFence', () => {
  it('routes csv and tsv by fence language', () => {
    expect(tableFromFence('csv', 'a,b\n1,2').columns).toEqual(['a', 'b'])
    expect(tableFromFence('tsv', 'a\tb\n1\t2').columns).toEqual(['a', 'b'])
  })

  // "data" means "show this as a table" whatever the author had to hand.
  it('accepts either JSON or delimited text under data', () => {
    expect(tableFromFence('data', '[{"a":1}]').columns).toEqual(['a'])
    expect(tableFromFence('data', 'a,b\n1,2').columns).toEqual(['a', 'b'])
  })

  it('ignores fences it does not own', () => {
    expect(tableFromFence('python', 'print(1)')).toBeNull()
    expect(tableFromFence('csv', '')).toBeNull()
  })
})
