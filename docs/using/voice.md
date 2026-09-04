# Voice chat

Soulacy voice can use a free local speech sidecar while your agent continues to
use any configured LLM provider.

## Connect local speech

1. Install, configure, and start the bundled adapter with
   `sy voice enable --recipe auto`. Apple Silicon Macs use MLX Whisper.
   The portable recipe requires an existing `whisper-cli` build and starts with
   `sy voice start --recipe whisper-kokoro --whisper-model /path/to/model.bin`.

GPU acceleration is selected automatically when available: Metal/MLX and MPS
on Apple Silicon, CUDA on supported NVIDIA hosts, or the GPU backend compiled
into whisper.cpp. Use `sy voice enable --recipe auto --accelerator cpu` only
when you need to force the compatibility fallback.
2. In Chat, click **🎤** and choose **Connect a voice sidecar**.
3. Enter its URL (normally `http://127.0.0.1:8081`) and optional voice name.
4. Save, restart the gateway, and return to Chat.

CLI users can do the same with:

```bash
sy voice providers
sy voice configure --sidecar-url http://127.0.0.1:8081 --voice af_heart
sy voice test
```

## Talk to an agent

Click **🎤** to open a continuous voice session. Soulacy starts listening
immediately, detects the end of your turn after a short silence, transcribes the
recording, and sends that text through the current agent and LLM. When the LLM
has completed its response, the sidecar speaks a short conversational version
and Soulacy automatically resumes listening for the next turn. The same LLM
call also returns the complete written answer for Chat, so tables, citations,
and supporting detail remain available without being read aloud. Voice turns
carry private request metadata; guidance is not appended to the visible user
message and does not affect later typed turns.

The microphone is paused while the agent is thinking or speaking so the agent
does not transcribe its own answer. A quiet repeating tone confirms that the
LLM is still working after transcription; the voice mute control also silences
this cue. Use the center control to finish a turn manually, the playback
controls to pause or stop an answer, and **End session** to stop continuous
listening. The transcript and original answer remain in the same chat history
as typed turns; audio is not persisted.

The realtime OpenAI mode remains available for users who explicitly configure
it. See [Voice configuration](../configuration/voice.md) for both modes, the
sidecar HTTP contract, security limits, and troubleshooting.
