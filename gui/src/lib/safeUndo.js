const statuses = ['draft', 'applied', 'partial', 'undone', 'needs_review', 'running']
const actionStatuses = ['pending', 'applied', 'undone', 'needs_review', 'running']
const bytes = s => typeof s === 'string' ? new TextEncoder().encode(s).length : Infinity
const invalid = () => { throw new Error('The gateway returned an invalid Safe Undo receipt. Refresh before proceeding.') }
export const statusLabel = s => ({ draft: 'Awaiting review', applied: 'Applied', partial: 'Partly completed', undone: 'Undone', needs_review: 'Check external state', running: 'Check external state', pending: 'Not applied' }[s] || s)

export function validateJobs(value, agentID) {
  if (!Array.isArray(value?.jobs) || value.jobs.length > 100 || new Set(value.jobs.map(j => j.id)).size !== value.jobs.length) invalid()
  for (const j of value.jobs) {
    if (!j.id || bytes(j.id) > 128 || j.agent_id !== agentID || bytes(j.title) > 200 || !statuses.includes(j.status) ||
        !Number.isInteger(j.action_count) || j.action_count < 1 || j.action_count > 8) invalid()
  }
  return value.jobs
}

export function validateJob(j, agentID, id) {
  if (!j || j.agent_id !== agentID || j.id !== id || bytes(j.title) > 200 || !statuses.includes(j.status) ||
      typeof j.undo_started !== 'boolean' || !Array.isArray(j.actions) || !j.actions.length || j.actions.length > 8 ||
      new Set(j.actions.map(a => a.resource_id)).size !== j.actions.length) invalid()
  for (const a of j.actions) {
    if (bytes(a.name) > 120 || !a.resource_id || !actionStatuses.includes(a.status) || typeof a.reconciled !== 'boolean') invalid()
    if (a.kind === 'webdav_text') {
      if (bytes(a.before_text) > 65536 || bytes(a.after_text) > 65536) invalid()
    } else if (a.kind === 'json_record') {
      if (!Array.isArray(a.fields) || !a.fields.length || a.fields.length > 32 || new Set(a.fields.map(f => f.name)).size !== a.fields.length) invalid()
      for (const f of a.fields) {
        if (!f.name || bytes(f.name) > 128) invalid()
        for (const v of [f.before, f.after]) {
          if (typeof v?.exists !== 'boolean' || (v.exists && (!v.display || bytes(v.display) > 65536))) invalid()
        }
      }
    } else invalid()
  }
  return j
}

export function validateReview(r, agentID, id, direction, now = Date.now()) {
  validateJob(r?.job, agentID, id)
  const expires = Date.parse(r.expires_at)
  if (!['apply', 'undo', 'reconcile'].includes(direction) || r.direction !== direction || !r.token || bytes(r.token) > 128 ||
      !Number.isFinite(expires) || expires <= now || expires > now + 301000 || !r.warning ||
      !Array.isArray(r.steps) || !r.steps.length || r.steps.length > 8 || new Set(r.steps.map(s => s.index)).size !== r.steps.length) invalid()
  const wanted = direction === 'apply' ? ['pending'] : direction === 'undo' ? ['applied'] : ['needs_review', 'running']
  const expected = r.job.actions.map((a, i) => wanted.includes(a.status) ? i : -1).filter(i => i >= 0)
  if (direction === 'undo') expected.reverse()
  if (JSON.stringify(expected) !== JSON.stringify(r.steps.map(s => s.index))) invalid()
  for (const s of r.steps) {
    if (!Number.isInteger(s.index) || !r.job.actions[s.index] ||
        !(direction === 'apply' ? ['applied'] : direction === 'undo' ? ['undone'] : ['pending', 'applied', 'undone']).includes(s.result)) invalid()
  }
  return r
}

export function directions(job) {
  if (!job) return []
  if (job.actions.some(a => ['needs_review', 'running'].includes(a.status))) return ['reconcile']
  return [!job.undo_started && job.actions.some(a => a.status === 'pending') && 'apply', job.actions.some(a => a.status === 'applied') && 'undo'].filter(Boolean)
}
export const displayValue = v => v.exists ? v.display : '(field absent)'
