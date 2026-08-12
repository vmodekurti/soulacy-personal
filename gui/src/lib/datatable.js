// datatable.js — turn a dataset an agent emitted into rows and columns.
//
// Agents produce tabular data constantly: a screening result, a comparison, a
// ledger. Sent as a ```csv or ```json fence it arrived as highlighted source —
// technically the data, practically unreadable, and impossible to scan down a
// column. This module is the parsing half; markdown.js builds the DOM from it.
//
// Parsing lives here, apart from the renderer, so the fiddly part — quoted
// fields, embedded commas, embedded newlines — is testable without a DOM.
//
// Deliberately does NOT fetch anything. A linked .csv on another origin would
// mean a cross-origin request from the chat bubble on behalf of untrusted
// content, which is a much bigger decision than rendering a table; only data
// the agent actually put in the message is rendered.

/** Largest table we will build. Beyond this the browser, not the reader, is
 * the limiting factor — and a 50k-row table helps nobody. */
export const MAX_TABLE_ROWS = 500

/**
 * Parse delimited text into { columns, rows }.
 *
 * Handles the parts of RFC 4180 that actually show up: quoted fields, commas
 * and newlines inside quotes, and "" as an escaped quote. A naive split on
 * commas mangles exactly the data worth tabulating — company names, addresses,
 * anything with a comma in it.
 *
 * @param {string} text
 * @param {string} [delimiter] defaults to ','; tab is auto-detected
 * @returns {{columns: string[], rows: string[][], truncated: boolean}}
 */
export function parseDelimited(text, delimiter) {
  const src = String(text == null ? '' : text).replace(/\r\n?/g, '\n').replace(/\n+$/, '')
  if (src.trim() === '') return { columns: [], rows: [], truncated: false }

  const delim = delimiter || detectDelimiter(src)
  const records = []
  let field = ''
  let record = []
  let inQuotes = false

  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (inQuotes) {
      if (c === '"') {
        if (src[i + 1] === '"') { field += '"'; i++ } // "" is one literal quote
        else inQuotes = false
      } else {
        field += c
      }
      continue
    }
    if (c === '"' && field === '') { inQuotes = true; continue }
    if (c === delim) { record.push(field); field = ''; continue }
    if (c === '\n') { record.push(field); records.push(record); record = []; field = ''; continue }
    field += c
  }
  record.push(field)
  records.push(record)

  const header = (records.shift() || []).map((h) => h.trim())
  const truncated = records.length > MAX_TABLE_ROWS
  return {
    columns: header,
    rows: records.slice(0, MAX_TABLE_ROWS).map((r) => padTo(r, header.length)),
    truncated,
  }
}

// Tab-separated data is common enough (spreadsheet copy/paste) that guessing
// beats making the author declare it. The header line decides: whichever
// delimiter appears more often there is the one in use.
function detectDelimiter(src) {
  const firstLine = src.split('\n', 1)[0] || ''
  return (firstLine.split('\t').length > firstLine.split(',').length) ? '\t' : ','
}

function padTo(row, n) {
  const out = row.slice(0, n)
  while (out.length < n) out.push('')
  return out
}

/**
 * Parse a JSON dataset into { columns, rows }.
 *
 * Accepts the two shapes models actually emit: an array of objects
 * ([{a:1,b:2}, …]) and an explicit {columns, rows}. An array of objects with
 * differing keys keeps the union, in first-seen order, so a row missing a field
 * shows an empty cell rather than silently shifting the columns along.
 *
 * @param {string|any} input JSON text or an already-parsed value
 * @returns {{columns: string[], rows: string[][], truncated: boolean}|null} null when it is not tabular
 */
export function parseJSONTable(input) {
  let data = input
  if (typeof input === 'string') {
    try { data = JSON.parse(input) } catch (_) { return null }
  }
  if (!data) return null

  if (!Array.isArray(data) && Array.isArray(data.rows)) {
    const columns = Array.isArray(data.columns) ? data.columns.map(String) : []
    const rows = data.rows.slice(0, MAX_TABLE_ROWS).map((r) =>
      Array.isArray(r) ? r.map(cellText) : columns.map((c) => cellText(r && r[c])))
    return { columns, rows, truncated: data.rows.length > MAX_TABLE_ROWS }
  }

  if (!Array.isArray(data) || data.length === 0) return null
  if (!data.every((r) => r && typeof r === 'object' && !Array.isArray(r))) return null

  const columns = []
  for (const row of data) {
    for (const k of Object.keys(row)) if (!columns.includes(k)) columns.push(k)
  }
  if (columns.length === 0) return null

  return {
    columns,
    rows: data.slice(0, MAX_TABLE_ROWS).map((row) => columns.map((c) => cellText(row[c]))),
    truncated: data.length > MAX_TABLE_ROWS,
  }
}

// A nested object or array is shown as compact JSON rather than "[object
// Object]", which tells the reader nothing about what is in the cell.
function cellText(v) {
  if (v == null) return ''
  if (typeof v === 'object') {
    try { return JSON.stringify(v) } catch (_) { return String(v) }
  }
  return String(v)
}

/**
 * Parse a fenced block into a table, choosing by fence language.
 * @param {string} lang fence language, e.g. "csv", "tsv", "data"
 * @param {string} src fence body
 * @returns {{columns: string[], rows: string[][], truncated: boolean}|null}
 */
export function tableFromFence(lang, src) {
  const l = String(lang || '').toLowerCase()
  if (l === 'csv') return nonEmpty(parseDelimited(src, ','))
  if (l === 'tsv') return nonEmpty(parseDelimited(src, '\t'))
  if (l === 'data') {
    // "data" means "show this as a table" whatever the author had to hand, so
    // try JSON first and fall back to delimited text.
    const asJSON = parseJSONTable(src)
    if (asJSON) return nonEmpty(asJSON)
    return nonEmpty(parseDelimited(src))
  }
  return null
}

function nonEmpty(t) {
  if (!t || t.columns.length === 0) return null
  return t
}
