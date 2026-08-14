// Realtime voice helpers (Story 11, docs/VOICE_SPIKE.md). Pure functions —
// unit-tested in voice.test.js. The WebRTC glue in Chat.svelte stays thin;
// everything decidable lives here.

/** Panel states. */
export const VOICE_STATES = ['unavailable', 'idle', 'connecting', 'live', 'error']

/**
 * Voice panel state machine. Events: status(available), start, connected,
 * stop, fail, retry.
 */
export function nextVoiceState(state, event) {
  switch (event.type) {
    case 'status':
      return event.available ? (state === 'unavailable' ? 'idle' : state) : 'unavailable'
    case 'start':
      return state === 'idle' ? 'connecting' : state
    case 'connected':
      return state === 'connecting' ? 'live' : state
    case 'stop':
      return state === 'live' || state === 'connecting' ? 'idle' : state
    case 'fail':
      return state === 'unavailable' ? state : 'error'
    case 'retry':
      return state === 'error' ? 'idle' : state
    default:
      return state
  }
}

/** SDP exchange endpoint for OpenAI Realtime WebRTC. */
export function realtimeCallURL(model, baseURL = 'https://api.openai.com') {
  return `${baseURL}/v1/realtime/calls?model=${encodeURIComponent(model || 'gpt-realtime-mini')}`
}

/**
 * Classify a Realtime data-channel event into what the panel renders.
 * Returns {kind, text?, usage?}:
 *   user_transcript      — completed transcription of the user's speech
 *   assistant_delta      — streaming assistant transcript fragment
 *   assistant_done       — assistant turn finished
 *   usage                — token usage for the finished response
 *   other                — ignored
 */
export function classifyRealtimeEvent(evt) {
  if (!evt || typeof evt.type !== 'string') return { kind: 'other' }
  switch (evt.type) {
    case 'conversation.item.input_audio_transcription.completed':
      return { kind: 'user_transcript', text: evt.transcript || '' }
    case 'response.output_audio_transcript.delta':
    case 'response.audio_transcript.delta': // pre-GA event name
      return { kind: 'assistant_delta', text: evt.delta || '' }
    case 'response.output_audio_transcript.done':
    case 'response.audio_transcript.done':
      return { kind: 'assistant_done', text: evt.transcript || '' }
    case 'response.done': {
      const u = evt.response?.usage
      return u ? { kind: 'usage', usage: u } : { kind: 'other' }
    }
    default:
      return { kind: 'other' }
  }
}

/** Accumulate usage events into a session total. */
export function addUsage(total, usage) {
  const t = { input: total?.input || 0, output: total?.output || 0 }
  t.input += usage?.input_tokens || 0
  t.output += usage?.output_tokens || 0
  return t
}

/** Compact cost/usage label for the panel, '' when nothing used yet. */
export function voiceUsageLabel(total) {
  if (!total || (!total.input && !total.output)) return ''
  return `↑${total.input} ↓${total.output} tok`
}

/** Human hint for each panel state. */
export function voiceHint(state, detail = '') {
  switch (state) {
    case 'unavailable':
	  return detail || 'Voice is not configured. Click to connect a local speech sidecar.'
    case 'idle':
      return 'Start a voice conversation'
    case 'connecting':
      return 'Connecting…'
    case 'live':
      return 'Live — click to stop'
    case 'error':
      return detail || 'Voice session failed — click to retry'
    default:
      return ''
  }
}

/**
 * Advance the local sidecar's voice-activity detector.
 * Keeping this decision pure makes silence handling deterministic and testable.
 */
export function updateVoiceActivity(state, rms, now, options = {}) {
  const threshold = options.threshold ?? 0.018
  const silenceMs = options.silenceMs ?? 900
  const minTurnMs = options.minTurnMs ?? 450
  const maxTurnMs = options.maxTurnMs ?? 60000
  const maxIdleMs = options.maxIdleMs ?? 30000
  const next = {
    startedAt: state?.startedAt ?? now,
    heardSpeech: !!state?.heardSpeech,
    silenceSince: state?.silenceSince ?? 0,
  }

  if (rms >= threshold) {
    next.heardSpeech = true
    next.silenceSince = 0
  } else if (next.heardSpeech) {
    if (!next.silenceSince) next.silenceSince = now
    if (now - next.startedAt >= minTurnMs && now - next.silenceSince >= silenceMs) {
      return { state: next, action: 'complete' }
    }
  }

  if (next.heardSpeech && now - next.startedAt >= maxTurnMs) {
    return { state: next, action: 'complete' }
  }
  if (!next.heardSpeech && now - next.startedAt >= maxIdleMs) {
    return { state: { startedAt: now, heardSpeech: false, silenceSince: 0 }, action: 'reset' }
  }
  return { state: next, action: 'continue' }
}

/**
 * Turn the rich Chat response into text that sounds natural when spoken.
 * The original response remains untouched in Chat; this removes visual-only
 * Markdown, citations, source lists, URLs, code, and emoji before TTS.
 */
export function speechText(text) {
  let clean = String(text || '').replace(/\r\n?/g, '\n')
  if (!clean.trim()) return ''

  // A sources appendix is useful on screen but painful when read aloud.
  clean = clean.replace(/\n\s*#{0,6}\s*(?:sources?|references?)\s*:?\s*\n[\s\S]*$/i, '\n')

  // Do not read code character-by-character. Preserve a useful spoken cue.
  let omittedCode = false
  clean = clean.replace(/```[\s\S]*?```/g, () => {
    omittedCode = true
    return '\n'
  })

  clean = clean
    .replace(/<[^>]+>/g, ' ')
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/<https?:\/\/[^>]+>/gi, ' ')
    .replace(/https?:\/\/\S+/gi, ' ')
    .replace(/^\s*\|?(?:\s*:?-{3,}:?\s*\|)+\s*$/gm, '')
    .replace(/\|/g, ', ')
    .replace(/^\s*#{1,6}\s*/gm, '')
    .replace(/^\s*>\s?/gm, '')
    .replace(/^\s*(?:[-+*]|\d+[.)])\s+/gm, '')
    .replace(/\[(?:\d+(?:\s*[-,]\s*\d+)*)\]/g, '')
    .replace(/[`*_~]/g, '')
    .replace(/[\p{Extended_Pictographic}\uFE0F]/gu, '')
    .replace(/^\s*(?:-{3,}|_{3,}|\*{3,})\s*$/gm, '')
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{2,}/g, '. ')
    .replace(/\n/g, ' ')
    .replace(/\s+([,.;:!?])/g, '$1')
    .replace(/([,.;:!?]){2,}/g, '$1')
    .replace(/\s+/g, ' ')
    .trim()

  if (omittedCode) {
    const cue = 'The code example is available in the text response.'
    clean = clean ? `${clean} ${cue}` : cue
  }
  return clean
}

/** Split a completed, speech-safe reply into short TTS requests. */
export function speechChunks(text, maxChars = 360) {
  const clean = speechText(text)
  if (!clean) return []
  const sentences = clean.match(/[^.!?]+[.!?]+|[^.!?]+$/g) || [clean]
  const chunks = []
  let current = ''
  const flush = () => {
    if (current.trim()) chunks.push(current.trim())
    current = ''
  }
  for (const sentence of sentences) {
    let part = sentence.trim()
    if (!part) continue
    if (current && current.length + 1 + part.length <= maxChars) {
      current += ` ${part}`
      continue
    }
    flush()
    while (part.length > maxChars) {
      let cut = part.lastIndexOf(' ', maxChars)
      if (cut < Math.floor(maxChars / 2)) cut = maxChars
      chunks.push(part.slice(0, cut).trim())
      part = part.slice(cut).trim()
    }
    current = part
  }
  flush()
  return chunks
}
