// Log display helpers for the Logs page — kept framework-free so they can be
// unit-tested with vitest.

// Matches ANSI escape sequences: CSI (colors/cursor, e.g. \x1b[31m), OSC
// (terminal titles, e.g. \x1b]0;title\x07), and single-char escapes.
// eslint-disable-next-line no-control-regex
const ANSI_RE = /\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])/g

/** Strip ANSI escape codes so raw terminal output renders cleanly. */
export function stripAnsi(line = '') {
  return line.replace(ANSI_RE, '')
}

/**
 * Best-effort severity detection across the log formats the gateway emits:
 * zap JSON ({"level":"error",...}), zap console (tab-separated ERROR), and
 * logfmt (level=error). Returns 'error' | 'warn' | 'info' | 'debug' | 'other'.
 */
export function logLevel(line = '') {
  const l = stripAnsi(line).toLowerCase()
  if (l.includes('"error"') || l.includes('error\t') || l.includes('level=error') || l.includes('[error]')) return 'error'
  if (l.includes('"warn"')  || l.includes('warn\t')  || l.includes('level=warn')  || l.includes('[warn]'))  return 'warn'
  if (l.includes('"info"')  || l.includes('info\t')  || l.includes('level=info')  || l.includes('[info]'))  return 'info'
  if (l.includes('"debug"') || l.includes('debug\t') || l.includes('level=debug') || l.includes('[debug]')) return 'debug'
  return 'other'
}

export const LEVEL_COLORS = {
  error: '#f06060',
  warn:  '#f0a060',
  info:  '#4caf82',
  debug: '#555a7a',
  other: '#b0b5d8',
}

export const LEVEL_BADGES = {
  error: 'ERR',
  warn:  'WRN',
  info:  'INF',
  debug: 'DBG',
  other: '   ',
}
