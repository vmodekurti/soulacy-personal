import { describe, it, expect } from 'vitest'
import {
  nextVoiceState, realtimeCallURL, classifyRealtimeEvent,
  addUsage, voiceUsageLabel, voiceHint,
  updateVoiceActivity, speechText, speechChunks,
} from './voice.js'

describe('nextVoiceState', () => {
  it('walks the happy path idle → connecting → live → idle', () => {
    let s = 'idle'
    s = nextVoiceState(s, { type: 'start' })
    expect(s).toBe('connecting')
    s = nextVoiceState(s, { type: 'connected' })
    expect(s).toBe('live')
    s = nextVoiceState(s, { type: 'stop' })
    expect(s).toBe('idle')
  })

  it('status toggles availability', () => {
    expect(nextVoiceState('unavailable', { type: 'status', available: true })).toBe('idle')
    expect(nextVoiceState('idle', { type: 'status', available: false })).toBe('unavailable')
    expect(nextVoiceState('live', { type: 'status', available: true })).toBe('live')
  })

  it('failures land in error and retry recovers', () => {
    expect(nextVoiceState('connecting', { type: 'fail' })).toBe('error')
    expect(nextVoiceState('error', { type: 'retry' })).toBe('idle')
    expect(nextVoiceState('unavailable', { type: 'fail' })).toBe('unavailable')
  })

  it('start only fires from idle', () => {
    expect(nextVoiceState('unavailable', { type: 'start' })).toBe('unavailable')
    expect(nextVoiceState('live', { type: 'start' })).toBe('live')
  })
})

describe('realtimeCallURL', () => {
  it('builds the SDP exchange URL with the model', () => {
    expect(realtimeCallURL('gpt-realtime-mini'))
      .toBe('https://api.openai.com/v1/realtime/calls?model=gpt-realtime-mini')
  })
  it('defaults the model and encodes it', () => {
    expect(realtimeCallURL('')).toContain('model=gpt-realtime-mini')
    expect(realtimeCallURL('a b')).toContain('model=a%20b')
  })
})

describe('classifyRealtimeEvent', () => {
  it('maps user transcription completion', () => {
    expect(classifyRealtimeEvent({
      type: 'conversation.item.input_audio_transcription.completed',
      transcript: 'hello there',
    })).toEqual({ kind: 'user_transcript', text: 'hello there' })
  })

  it('maps assistant transcript deltas (GA and pre-GA names)', () => {
    expect(classifyRealtimeEvent({ type: 'response.output_audio_transcript.delta', delta: 'Hi' }))
      .toEqual({ kind: 'assistant_delta', text: 'Hi' })
    expect(classifyRealtimeEvent({ type: 'response.audio_transcript.delta', delta: 'Hi' }))
      .toEqual({ kind: 'assistant_delta', text: 'Hi' })
  })

  it('maps assistant done with full transcript', () => {
    expect(classifyRealtimeEvent({ type: 'response.output_audio_transcript.done', transcript: 'Hi!' }))
      .toEqual({ kind: 'assistant_done', text: 'Hi!' })
  })

  it('extracts usage from response.done', () => {
    const r = classifyRealtimeEvent({
      type: 'response.done',
      response: { usage: { input_tokens: 12, output_tokens: 34 } },
    })
    expect(r.kind).toBe('usage')
    expect(r.usage.output_tokens).toBe(34)
  })

  it('ignores unknown and malformed events', () => {
    expect(classifyRealtimeEvent({ type: 'rate_limits.updated' }).kind).toBe('other')
    expect(classifyRealtimeEvent(null).kind).toBe('other')
    expect(classifyRealtimeEvent({}).kind).toBe('other')
  })
})

describe('usage accumulation', () => {
  it('adds usage events into a running total', () => {
    let t = addUsage(null, { input_tokens: 10, output_tokens: 5 })
    t = addUsage(t, { input_tokens: 2, output_tokens: 3 })
    expect(t).toEqual({ input: 12, output: 8 })
  })

  it('labels usage compactly and stays quiet at zero', () => {
    expect(voiceUsageLabel({ input: 12, output: 8 })).toBe('↑12 ↓8 tok')
    expect(voiceUsageLabel(null)).toBe('')
    expect(voiceUsageLabel({ input: 0, output: 0 })).toBe('')
  })
})

describe('voiceHint', () => {
  it('uses the server detail when unavailable', () => {
    expect(voiceHint('unavailable', 'no API key configured')).toBe('no API key configured')
	expect(voiceHint('unavailable')).toContain('sidecar')
  })
  it('covers every state', () => {
    for (const s of ['idle', 'connecting', 'live', 'error']) {
      expect(voiceHint(s)).not.toBe('')
    }
  })
})

describe('updateVoiceActivity', () => {
  it('completes a spoken turn after sustained silence', () => {
    let result = updateVoiceActivity({ startedAt: 0 }, 0.04, 100)
    expect(result.state.heardSpeech).toBe(true)
    result = updateVoiceActivity(result.state, 0.002, 700)
    expect(result.action).toBe('continue')
    result = updateVoiceActivity(result.state, 0.002, 1700)
    expect(result.action).toBe('complete')
  })

  it('does not submit ambient silence and periodically resets its buffer', () => {
    const result = updateVoiceActivity({ startedAt: 10 }, 0.001, 30010)
    expect(result.action).toBe('reset')
    expect(result.state.heardSpeech).toBe(false)
  })

  it('keeps listening while the speaker is active', () => {
    const result = updateVoiceActivity({ startedAt: 0, heardSpeech: true }, 0.05, 1500)
    expect(result.action).toBe('continue')
    expect(result.state.silenceSince).toBe(0)
  })
})

describe('speechChunks', () => {
  it('turns rich Markdown into natural speech without changing the Chat reply', () => {
    const input = `## Result 🚀

**Fast** and [documented](https://example.com) [1].

\`\`\`js
console.log('screen only')
\`\`\`

### Sources
- https://example.com/source`

    expect(speechText(input)).toBe(
      'Result. Fast and documented. The code example is available in the text response.',
    )
    expect(input).toContain('console.log')
  })

  it('makes lists and tables speakable', () => {
    const input = `- First item
- Second item

| Metric | Value |
| --- | --- |
| Latency | 10 ms |`
    const spoken = speechText(input)
    expect(spoken).toContain('First item Second item')
    expect(spoken).toContain('Metric, Value')
    expect(spoken).toContain('Latency, 10 ms')
    expect(spoken).not.toContain('|')
  })

  it('sanitizes speech before splitting it into synthesis requests', () => {
    expect(speechChunks('**Hello** [world](https://example.com).'))
      .toEqual(['Hello world.'])
  })

  it('returns short sentence-oriented chunks for faster first audio', () => {
    expect(speechChunks('First sentence. Second sentence! Third? ', 32))
      .toEqual(['First sentence. Second sentence!', 'Third?'])
  })

  it('bounds long text without losing content', () => {
    const input = 'A'.repeat(25)
    const chunks = speechChunks(input, 10)
    expect(chunks.every(chunk => chunk.length <= 10)).toBe(true)
    expect(chunks.join('')).toBe(input)
  })
})
