import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const chat = readFileSync(fileURLToPath(new URL('./Chat.svelte', import.meta.url)), 'utf8')

describe('Chat voice sidecar recipes', () => {
  it('renders every recommendation as a selectable control', () => {
    for (const recipe of ['mlx-kokoro', 'whisper-kokoro', 'custom']) {
      expect(chat).toContain(`selectVoiceRecipe('${recipe}')`)
      expect(chat).toContain(`voiceSetupRecipe === '${recipe}'`)
    }
    expect(chat.match(/class="voice-recipe"/g)).toHaveLength(3)
    expect(chat.match(/role="radio"/g)).toHaveLength(3)
	expect(chat).toContain('MLX Whisper + Kokoro — Apple Silicon')
  })

  it('applies recipe-specific defaults before saving', () => {
    expect(chat).toContain("voice: 'af_heart'")
    expect(chat).toContain('voiceSetupURL = preset.url')
    expect(chat).toContain('voiceSetupName = preset.voice')
	expect(chat).toContain('sy voice enable --recipe auto')
  })

  it('exposes cancellable playback controls for local responses', () => {
	expect(chat).toContain('speechChunks(reply)')
	expect(chat).toContain("spokenReply = res.spoken_reply || ''")
	expect(chat).toContain("responseMode === 'voice' ? (spokenReply || replyText) : replyText")
	expect(chat).toContain('pauseVoiceResponse')
	expect(chat).toContain('resumeVoiceResponse')
	expect(chat).toContain('stopVoiceResponse')
	expect(chat).toContain('Stop this response')
	expect(chat).toContain('END SESSION')
	expect(chat).toContain('toggleVoiceMute')
	expect(chat).toContain('voiceSessionOpen')
  })

  it('continuously listens, submits after silence, and resumes after playback', () => {
	expect(chat).toContain('startVoiceActivityDetection')
	expect(chat).toContain('updateVoiceActivity')
	expect(chat).toContain("result.action === 'complete'")
	expect(chat).toContain('voiceConversationActive && voiceState')
	expect(chat).toContain('setTimeout(() => startSidecarVoice(), 120)')
  })

  it('plays a local waiting cue while the LLM is thinking', () => {
	expect(chat).toContain('startVoiceThinkingSound()')
	expect(chat).toContain('stopVoiceThinkingSound()')
	expect(chat).toContain('voiceThinkingTimer = window.setInterval')
	expect(chat).toContain("oscillator.type = 'sine'")
	expect(chat).toContain('if (voiceMuted) stopVoiceThinkingSound()')
  })

  it('shows the selected local acceleration backends', () => {
	expect(chat).toContain('api.voice.capabilities()')
	expect(chat).toContain('caps.stt_accelerator')
	expect(chat).toContain('caps.tts_device')
	expect(chat).toContain('voice-session-backend')
  })
})
