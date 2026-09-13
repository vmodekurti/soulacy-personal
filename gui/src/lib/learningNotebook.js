const bytes = s => typeof s === 'string' ? new TextEncoder().encode(s).length : Infinity
export const lessonStatuses = ['pending', 'active', 'superseded', 'archived', 'rejected']
export const lessonStatusLabel = s => ({ pending: 'Needs review', active: 'In use', superseded: 'Replaced', archived: 'Disabled', rejected: 'Rejected' }[s] || s)
const invalid = () => { throw new Error('Invalid learning record. Refresh before reviewing it.') }
export function validateLesson(l, agentID, id = l?.id) {
  if (!l || !id || l.id !== id || l.agent_id !== agentID || bytes(id) > 128 || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(l.key) ||
      !['preference', 'fact', 'skill'].includes(l.kind) || !lessonStatuses.includes(l.status) ||
      !Number.isInteger(l.version) || l.version < 1 || !l.title || bytes(l.title) > 120 ||
      !l.trigger || bytes(l.trigger) > 400 || !l.content || bytes(l.content) > 6000 || !l.verification || bytes(l.verification) > 1000 ||
      bytes(l.pitfalls || '') > 1500 || bytes(l.base_id || '') > 128 ||
      !Array.isArray(l.sources) || l.sources.length < 1 || l.sources.length > 6 ||
      l.sources.some(s => !['user', 'tool'].includes(s.kind) || !s.id || bytes(s.text) > 600) ||
      !Array.isArray(l.citations) || l.citations.length !== l.sources.length ||
      l.citations.some((c, i) => c.source_id !== l.sources[i].id || c.quote !== l.sources[i].text) ||
      ![l.uses, l.helpful, l.unhelpful].every(n => Number.isSafeInteger(n) && n >= 0)) invalid()
  return l
}
export function validateLessons(value, agentID) {
  if (!Array.isArray(value?.lessons) || value.lessons.length > 100 || new Set(value.lessons.map(l => l.id)).size !== value.lessons.length || typeof value.enabled !== 'boolean' || typeof value.auto_propose !== 'boolean' ||
      !Number.isInteger(value.total) || value.total < 0 || value.total > 2000 || !Number.isInteger(value.offset) || value.offset < 0 || value.offset > 2000 || value.lessons.length > Math.max(0, value.total - value.offset)) invalid()
  return value.lessons.map(l => validateLesson(l, agentID))
}
