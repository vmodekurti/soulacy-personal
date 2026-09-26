// What an agent is doing, in words a person can read.
//
// The gateway emits a flat stream of `llm.call`, `tool.call`, `tool.result`,
// `reasoning.*` and `error` events. Chat already knew how to turn those into
// English, but the vocabulary lived inside the component, so the Genie screen
// could not reuse it and simply threw every event away — you got a spinner,
// then a wall of text. These are the same functions, extracted and testable.
//
// Two ideas beyond a rename:
//
//   - **Group by step.** A run is not a list of events, it is a handful of
//     steps, each of which called a tool and got something back. Pairing a
//     `tool.call` with its `tool.result` turns forty rows into eight, and is
//     what stops the panel running off the bottom of the screen.
//   - **Say what is happening now.** A finished log answers "what did it do".
//     A person watching wants "what is it doing", which is one line.

/** Event types worth showing. Everything else is machinery. */
export const RUN_EVENT_TYPES = [
  'llm.call', 'llm.result', 'tool.call', 'tool.result', 'tool.log',
  'error', 'reasoning.start', 'reasoning.step', 'reasoning.result',
]

export function isRunEvent(ev) {
  return RUN_EVENT_TYPES.includes(ev?.type || '')
}

function snippet(text, max = 200) {
  const s = String(text ?? '').replace(/\s+/g, ' ').trim()
  return s.length > max ? s.slice(0, max) + '…' : s
}

/** A tool name as a person would say it: `web_search` → `web search`. */
export function humanTool(name) {
  return String(name || 'a tool').replace(/^mcp__[^_]+__/, '').replaceAll('_', ' ')
}

export function eventTitle(ev) {
  const p = ev?.payload || {}
  switch (ev?.type) {
    case 'llm.call': return `Thinking · turn ${p.turn ?? '?'}`
    case 'llm.result': return p.tool_calls
      ? `Decided to use ${p.tool_calls} tool${p.tool_calls === 1 ? '' : 's'}`
      : 'Ready to answer'
    case 'tool.call': return `Using ${humanTool(p.name)}`
    case 'tool.result': return `${humanTool(p.name)} answered`
    case 'tool.log': return 'Tool progress'
    case 'reasoning.start': return `Planning (${p.strategy || 'steps'})`
    case 'reasoning.step': return `${p.recovery ? 'Recovering' : 'Step'} ${p.index ?? ''}`.trim()
    case 'reasoning.result': return `Planning done — ${p.steps ?? 0} step${p.steps === 1 ? '' : 's'}`
    case 'error': return `Problem${p.stage ? ` in ${p.stage}` : ''}`
    default: return ev?.type || 'event'
  }
}

export function eventDetail(ev) {
  const p = ev?.payload || {}
  switch (ev?.type) {
    case 'tool.call': return snippet(JSON.stringify(p.arguments || {}), 200)
    case 'tool.result': return snippet(p.content || '', 240)
    case 'tool.log': return snippet(typeof p === 'string' ? p : p.line || JSON.stringify(p), 240)
    case 'error': return snippet(p.error || p.message || JSON.stringify(p), 240)
    case 'llm.result': return `${p.duration_ms ?? 0}ms · ${p.input_tokens ?? 0} in / ${p.output_tokens ?? 0} out`
    case 'reasoning.step': return snippet(p.recovery ? p.observation || p.thought || '' : p.thought || '', 240)
    default: return ''
  }
}

/** The complete payload, for someone who opens a step. */
export function eventFullDetail(ev) {
  const p = ev?.payload || {}
  switch (ev?.type) {
    case 'tool.call': return JSON.stringify(p.arguments || {}, null, 2)
    case 'tool.result': return String(p.content ?? '')
    case 'tool.log': return typeof p === 'string' ? p : p.line || JSON.stringify(p, null, 2)
    case 'error': return String(p.error || p.message || JSON.stringify(p, null, 2))
    case 'reasoning.step': return String(p.thought || '')
    case 'llm.call':
    case 'llm.result': return JSON.stringify(p, null, 2)
    default: return ''
  }
}

export function eventKind(ev) {
  const type = ev?.type || ''
  if (ev?.type === 'reasoning.step' && ev.payload?.recovery) return 'recovery'
  if (type.includes('error')) return 'err'
  if (type.startsWith('tool.')) return 'tool'
  if (type.startsWith('llm.')) return 'llm'
  return ''
}

/**
 * Fold a flat event list into steps.
 *
 * A `tool.call` and its `tool.result` are one step, not two rows — that
 * pairing is most of the reason the old panel was so long. Model turns
 * become their own step so the reader can see the shape of the run:
 * think, use these tools, think again, answer.
 */
export function groupRunEvents(events = []) {
  const steps = []
  const byCallID = new Map()
  for (const ev of events) {
    if (!isRunEvent(ev)) continue
    const p = ev.payload || {}
    if (ev.type === 'tool.call') {
      const step = {
        kind: 'tool', name: p.name, title: eventTitle(ev), detail: eventDetail(ev),
        at: ev.timestamp, call: ev, result: null, failed: false, done: false,
      }
      if (p.id || p.call_id) byCallID.set(p.id || p.call_id, step)
      steps.push(step)
      continue
    }
    if (ev.type === 'tool.result') {
      const step = byCallID.get(p.call_id) || [...steps].reverse()
        .find((s) => s.kind === 'tool' && !s.done && (!p.name || s.name === p.name))
      if (step) {
        step.result = ev
        step.done = true
        step.failed = !!p.is_error
        step.detail = eventDetail(ev)
        step.title = p.is_error ? `${humanTool(p.name || step.name)} failed` : eventTitle(ev)
        continue
      }
      // A result with no call in view still deserves a row.
      steps.push({ kind: 'tool', name: p.name, title: eventTitle(ev), detail: eventDetail(ev), at: ev.timestamp, result: ev, done: true, failed: !!p.is_error })
      continue
    }
    if (ev.type === 'llm.result') {
      // The interesting half of a model turn is what it decided, which the
      // result carries; fold it into the turn the call opened.
      const turn = [...steps].reverse().find((s) => s.kind === 'model' && !s.done)
      if (turn) {
        turn.done = true
        turn.title = eventTitle(ev)
        turn.detail = eventDetail(ev)
        continue
      }
    }
    steps.push({
      kind: ev.type === 'llm.call' ? 'model' : eventKind(ev) || 'note',
      title: eventTitle(ev), detail: eventDetail(ev), at: ev.timestamp,
      call: ev, done: ev.type !== 'llm.call', failed: eventKind(ev) === 'err',
    })
  }
  return steps
}

/** One line: what is happening right now. */
export function liveActivity(events = []) {
  if (!events.length) return 'Starting…'
  const last = events[events.length - 1]
  const p = last.payload || {}
  switch (last.type) {
    case 'tool.call': return `Using ${humanTool(p.name)}…`
    case 'llm.call': return `Thinking${p.turn ? ` · turn ${p.turn}` : ''}…`
    case 'tool.result':
    case 'tool.log': return 'Reading what came back…'
    case 'llm.result': return p.tool_calls ? 'Getting ready to use tools…' : 'Writing the answer…'
    case 'reasoning.start':
    case 'reasoning.step': return 'Working out the next step…'
    case 'error': return 'Hit a problem — trying to recover…'
    default: return 'Working…'
  }
}

/** A one-line roll-up for the collapsed header. */
export function runSummary(events = []) {
  const steps = groupRunEvents(events)
  const tools = steps.filter((s) => s.kind === 'tool')
  const failed = tools.filter((s) => s.failed).length
  const turns = steps.filter((s) => s.kind === 'model').length
  if (!steps.length) return ''
  const parts = []
  if (tools.length) parts.push(`${tools.length} tool${tools.length === 1 ? '' : 's'}`)
  if (turns) parts.push(`${turns} turn${turns === 1 ? '' : 's'}`)
  if (failed) parts.push(`${failed} failed`)
  return parts.join(' · ')
}

/**
 * Whether a scroller is close enough to the bottom that following new content
 * is what the reader wants.
 *
 * The old panel scrolled to the bottom on every single event, which is why
 * reading it while a run was in flight was impossible: any attempt to look at
 * an earlier step was yanked away within the second. Scrolling up is a
 * deliberate act and must win until the reader comes back down.
 */
export function isPinnedToBottom(el, slack = 48) {
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight <= slack
}
