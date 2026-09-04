// Workboard column model shared by Workboard.svelte and its tests.

/** Kanban columns in board order. Mirrors internal/workboard statuses. */
export const STATUSES = ['todo', 'running', 'needs_review', 'done', 'failed']

export const STATUS_LABELS = {
  todo:         'Todo',
  running:      'Running',
  needs_review: 'Needs Review',
  done:         'Done',
  failed:       'Failed',
}

/**
 * Returns the status one column to the left (dir=-1) or right (dir=+1),
 * or null when the move would fall off the board or status is unknown.
 */
export function adjacentStatus(status, dir) {
  const i = STATUSES.indexOf(status)
  if (i === -1) return null
  const j = i + dir
  if (j < 0 || j >= STATUSES.length) return null
  return STATUSES[j]
}

/**
 * Whether a task can be (re)run right now: it needs an assigned agent and
 * must not already be running. The server is the final authority (409 on
 * duplicate concurrent runs) — this only drives button state.
 */
export function canRun(task) {
  if (!task) return false
  return Boolean(task.agent_id) && task.status !== 'running'
}

/** Label for the run action: Retry after a failure, Run otherwise. */
export function runLabel(task) {
  return task && task.status === 'failed' ? 'Retry' : 'Run'
}

// ── collaboration primitives (Story 14) ──────────────────────────────

/** Priorities in escalation order. */
export const PRIORITIES = ['low', 'normal', 'high', 'urgent']

/** Emoji/visual marker per priority ('' for normal keeps cards quiet). */
export function priorityBadge(p) {
  switch (p) {
    case 'low': return '▽'
    case 'high': return '▲'
    case 'urgent': return '‼'
    default: return ''
  }
}

/** "a, b, c" → ['a','b','c'] (trimmed, lowercased, empties dropped). */
export function parseTags(s) {
  return String(s || '')
    .split(',')
    .map(t => t.trim().toLowerCase())
    .filter(Boolean)
}

/** ['a','b'] → "a, b" for the editor input. */
export function formatTags(tags) {
  return (tags || []).join(', ')
}

/**
 * Compact due-date label + overdue flag for cards and the editor.
 * Returns { label, overdue } or null when no due date.
 */
export function dueInfo(dueAt, now = new Date()) {
  if (!dueAt) return null
  const due = new Date(dueAt)
  if (isNaN(due.getTime())) return null
  const overdue = due.getTime() < now.getTime()
  const days = Math.round((due.getTime() - now.getTime()) / 86400000)
  let label
  if (overdue) label = `overdue (${due.toLocaleDateString()})`
  else if (days === 0) label = 'due today'
  else if (days === 1) label = 'due tomorrow'
  else label = `due ${due.toLocaleDateString()}`
  return { label, overdue }
}

/** Short display name for an artifact: the file's base name. */
export function artifactName(path) {
  if (!path) return ''
  const parts = String(path).split('/')
  return parts[parts.length - 1] || path
}

/** Human file size: 0 B, 999 B, 1.2 KB, 3.4 MB, 5.6 GB. */
export function formatBytes(n) {
  n = Number(n) || 0
  if (n < 1000) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let u = -1
  do { n /= 1000; u++ } while (n >= 1000 && u < units.length - 1)
  return `${n.toFixed(1)} ${units[u]}`
}

/**
 * Download URL for an artifact (api_key query param mirrors how the
 * gateway accepts credentials on direct links).
 */
export function artifactDownloadUrl(artifactId, apiKey = '') {
  const base = `/api/v1/workboard/artifacts/${artifactId}/download`
  return apiKey ? `${base}?api_key=${encodeURIComponent(apiKey)}` : base
}

/** Groups a task list into { status: [tasks] } with every column present. */
export function groupByStatus(tasks) {
  const cols = {}
  for (const s of STATUSES) cols[s] = []
  for (const t of tasks || []) {
    if (cols[t.status]) cols[t.status].push(t)
  }
  return cols
}
