// Pure helpers for chat checkpoints & branching (Story 8).
// Tested in chatbranch.test.js.

/**
 * Maps a GUI message index to the persisted history entry it corresponds to.
 *
 * The GUI message list contains user/assistant turns plus local system
 * notices (errors) that are never persisted; the server's history contains
 * exactly the user/assistant turns in order. So the Nth user/assistant GUI
 * message corresponds to entries[N].
 *
 * Returns the entry id to fork at (inclusive), or null when the message has
 * no persisted counterpart (e.g. system rows, or history not yet written).
 */
export function entryIdForMessage(entries, messages, msgIndex) {
  if (!entries || !messages || msgIndex < 0 || msgIndex >= messages.length) return null
  const role = messages[msgIndex].role
  if (role !== 'user' && role !== 'assistant') return null
  let pos = -1
  for (let i = 0; i <= msgIndex; i++) {
    const r = messages[i].role
    if (r === 'user' || r === 'assistant') pos++
  }
  if (pos < 0 || pos >= entries.length) return null
  return entries[pos].id ?? null
}

/** "main", then "fork 1", "fork 2", … */
export function nextBranchLabel(branches) {
  return `fork ${branches.filter(b => b.label !== 'main').length + 1}`
}

/**
 * Converts persisted history entries into the GUI message shape, used when
 * opening a branch whose messages aren't in memory.
 */
export function entriesToMessages(entries) {
  return (entries || [])
    .filter(e => e.role === 'user' || e.role === 'assistant')
    .map(e => ({ role: e.role, text: e.content, ts: e.created_at ? new Date(e.created_at) : new Date() }))
}
