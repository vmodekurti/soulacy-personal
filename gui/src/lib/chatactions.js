// chatactions.js — pure helpers for the Chat workspace (Modern Chat Workspace epic).
// Kept framework-free so the logic is unit-testable without mounting Svelte.

// filterThreads returns the threads to show in the sidebar/tab strip: archived
// hidden unless showArchived, filtered by a case-insensitive query over title +
// agent name, and sorted pinned-first then by most-recent activity.
export function filterThreads(threads, query = '', showArchived = false, agentNameOf = (id) => id) {
  const q = (query || '').trim().toLowerCase()
  return (threads || [])
    .filter((t) => (showArchived ? true : !t.archived))
    .filter((t) => {
      if (!q) return true
      const hay = `${t.title || ''} ${agentNameOf(t.agentId) || ''}`.toLowerCase()
      return hay.includes(q)
    })
    .sort((a, b) => {
      if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1
      return (b.updatedAt || 0) - (a.updatedAt || 0)
    })
}

// suggestedPrompts returns starter prompts for an agent's empty state. It draws
// from the agent's declared `actions`/`suggestions` when present, otherwise a
// small sensible default set. Always returns up to `limit` non-empty strings.
export function suggestedPrompts(agent, limit = 4) {
  const out = []
  const push = (s) => {
    const v = String(s || '').trim()
    if (v && !out.includes(v)) out.push(v)
  }
  if (agent) {
    const lists = [agent.suggested_prompts, agent.suggestions, agent.actions, agent.examples]
    for (const list of lists) {
      if (Array.isArray(list)) {
        for (const item of list) push(typeof item === 'string' ? item : item?.prompt || item?.label || item?.text)
      }
    }
  }
  if (out.length === 0) {
    push('What can you help me with?')
    push('Summarize what you can do.')
    push('Walk me through an example task.')
  }
  return out.slice(0, limit)
}

// buildOverrides turns the in-chat model controls into the `overrides` object the
// chat API accepts, omitting blank fields. Returns null when nothing is set, so
// the agent's own config is used unchanged.
export function buildOverrides(controls = {}) {
  const o = {}
  const llm = {}
  const str = (v) => (typeof v === 'string' ? v.trim() : v)
  if (str(controls.provider)) llm.provider = str(controls.provider)
  if (str(controls.model)) llm.model = str(controls.model)
  if (controls.temperature !== '' && controls.temperature != null && !Number.isNaN(Number(controls.temperature))) {
    llm.temperature = Number(controls.temperature)
  }
  if (controls.topP !== '' && controls.topP != null && Number(controls.topP) > 0) {
    llm.top_p = Number(controls.topP)
  }
  if (controls.maxTokens !== '' && controls.maxTokens != null && Number(controls.maxTokens) > 0) {
    llm.max_tokens = Number(controls.maxTokens)
  }
  if (str(controls.responseFormat)) llm.response_format = str(controls.responseFormat)
  if (str(controls.reasoningEffort)) llm.reasoning_effort = str(controls.reasoningEffort)
  if (controls.presencePenalty !== '' && controls.presencePenalty != null && !Number.isNaN(Number(controls.presencePenalty))) {
    llm.presence_penalty = Number(controls.presencePenalty)
  }
  if (controls.frequencyPenalty !== '' && controls.frequencyPenalty != null && !Number.isNaN(Number(controls.frequencyPenalty))) {
    llm.frequency_penalty = Number(controls.frequencyPenalty)
  }
  if (str(controls.toolChoice)) llm.tool_choice = str(controls.toolChoice)
  if (Object.keys(llm).length) o.llm = llm
  return Object.keys(o).length ? o : null
}

// lastUserText returns the text of the most recent user message (for regenerate).
export function lastUserText(messages) {
  for (let i = (messages || []).length - 1; i >= 0; i--) {
    if (messages[i]?.role === 'user') return messages[i].text || ''
  }
  return ''
}

// truncateForRerun returns the messages kept when re-running from message index
// `mi`: everything strictly before it. The caller then sends the (edited) text
// of message `mi` as a fresh turn. Works for both "edit a prompt" and "regenerate
// the assistant reply above" (pass the index of the user message to replay).
export function truncateForRerun(messages, mi) {
  if (!Array.isArray(messages) || mi < 0) return messages || []
  return messages.slice(0, mi)
}

// isLongOutput reports whether a message body is tall enough to warrant the
// collapse affordance. Cheap heuristic on length / line count so we don't have
// to measure the DOM.
export function isLongOutput(text, { maxChars = 1400, maxLines = 24 } = {}) {
  if (!text) return false
  if (text.length > maxChars) return true
  let lines = 1
  for (let i = 0; i < text.length; i++) if (text[i] === '\n') lines++
  return lines > maxLines
}
