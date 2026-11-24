// Keeping the chat page responsive during long runs (#402).
//
// A long run streams thousands of tokens and activity events. Two things made
// each of them expensive: saving every conversation to browser storage on
// every update, and storing each reply's full activity record, tool output
// included. These helpers bound both.

const MAX_EVENTS = 80
const MAX_CHARS = 2000

function trimValue(value, maxChars, depth = 0) {
  if (typeof value === 'string') {
    return value.length > maxChars ? `${value.slice(0, maxChars)}… [${value.length - maxChars} more characters not kept]` : value
  }
  if (!value || typeof value !== 'object' || depth > 6) return value
  if (Array.isArray(value)) return value.slice(0, 50).map((v) => trimValue(v, maxChars, depth + 1))
  const out = {}
  for (const [k, v] of Object.entries(value)) out[k] = trimValue(v, maxChars, depth + 1)
  return out
}

/**
 * The activity record as it should be stored with a message: the most recent
 * events only, closed, with long tool output cut down. The live record in
 * memory is untouched; this is only what goes to storage.
 */
export function slimThinking(thinking, { maxEvents = MAX_EVENTS, maxChars = MAX_CHARS } = {}) {
  if (!thinking) return null
  const events = Array.isArray(thinking.events) ? thinking.events.slice(-maxEvents) : []
  return { ...thinking, open: false, events: events.map((ev) => trimValue(ev, maxChars)) }
}

/**
 * Run `fn` at most once per `wait` ms, with the latest arguments, on the
 * trailing edge. `flush()` runs a pending call now (used when the page is
 * being hidden or closed); `cancel()` drops it.
 */
export function throttleTrailing(fn, wait = 1500) {
  let timer = null
  let pending = null
  const run = () => {
    timer = null
    if (!pending) return
    const args = pending
    pending = null
    fn(...args)
  }
  const throttled = (...args) => {
    pending = args
    if (timer === null) timer = setTimeout(run, wait)
  }
  throttled.flush = () => {
    if (timer !== null) { clearTimeout(timer); timer = null }
    if (pending) { const args = pending; pending = null; fn(...args) }
  }
  throttled.cancel = () => {
    if (timer !== null) { clearTimeout(timer); timer = null }
    pending = null
  }
  return throttled
}

/**
 * Collect streamed tokens per conversation and hand them over in one batch,
 * so a burst of tokens causes one screen update rather than one each.
 */
export function tokenBatcher(apply, wait = 60) {
  const buffers = new Map()
  let timer = null
  const flush = () => {
    if (timer !== null) { clearTimeout(timer); timer = null }
    for (const [id, text] of buffers) if (text) apply(id, text)
    buffers.clear()
  }
  const push = (id, token) => {
    if (!id || !token) return
    buffers.set(id, (buffers.get(id) || '') + token)
    if (timer === null) timer = setTimeout(flush, wait)
  }
  return { push, flush }
}
