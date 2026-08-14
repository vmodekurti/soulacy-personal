# Voice

Voice is an optional speech layer around Soulacy's normal text-chat pipeline.
It does not need to use the same provider as the agent's LLM.

## Local sidecar (recommended)

Soulacy includes an HTTP adapter that can be installed into an isolated Python
environment. On Apple Silicon, `auto` selects MLX Whisper so transcription uses
Apple's MLX/Metal stack instead of requiring a separately compiled
whisper.cpp binary:

```bash
sy voice providers
brew install ffmpeg                         # macOS prerequisite
brew install python@3.12                    # required if Python 3.10–3.12 is absent
sy voice enable --recipe auto               # install, configure, and run in background
# restart Soulacy
sy voice test
```

Or click the microphone in **Chat** while voice is unavailable. The setup panel
shows the same recommendations and writes the configuration for you.

```yaml
voice:
  provider: sidecar
  sidecar_url: http://127.0.0.1:8081
  voice: af_heart             # optional TTS voice
  timeout: 60s
  allow_remote: false         # loopback only by default
```

Suggested local combinations:

| Recipe | STT | TTS | Notes |
|---|---|---|---|
| Apple Silicon (recommended on arm64 macOS) | [MLX Whisper](https://github.com/ml-explore/mlx-examples/tree/main/whisper) | [Kokoro](https://github.com/hexgrad/kokoro) | Native Apple MLX/Metal path; installed by `sy voice install --recipe mlx-kokoro` |
| Portable | [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | [Kokoro](https://github.com/hexgrad/kokoro) | Requires `whisper-cli` and a ggml model; pass the model to `sy voice start --recipe whisper-kokoro --whisper-model …` |

Hardware acceleration is automatic. MLX Whisper uses Metal on Apple Silicon;
Kokoro selects CUDA on NVIDIA hosts or MPS/Metal on Apple Silicon; and a
GPU-enabled whisper.cpp build uses its native GPU backend. CPU remains the safe
fallback. Override detection for troubleshooting with `--accelerator cpu`,
`--accelerator mps`, or `--accelerator cuda` on `sy voice start` or
`sy voice enable`. The sidecar capabilities response reports the selected STT
accelerator and TTS device.
| Custom | Any | Any | Wrap existing speech services behind the four endpoints below |

The models consume local CPU/GPU and disk space, but the selected Soulacy agent
can continue using Ollama, Google, NVIDIA, Anthropic, OpenAI, or any other text
provider.

The adapter warms both models before opening its HTTP port, so the first Chat
turn does not pay the model download/load cost. Initial `sy voice start` may
therefore take several minutes on a fresh installation; wait for `Voice models
ready.`. Set `SOULACY_VOICE_WARMUP=0` only when startup speed matters more than
first-response latency. Browser WebM/Opus audio is converted to 16 kHz mono WAV
with FFmpeg before invoking either STT backend.

## Sidecar HTTP contract

The configured base URL must expose:

### `GET /health`

Return any 2xx status when ready.

### `GET /capabilities`

```json
{
  "stt": true,
  "tts": true,
  "streaming": false,
  "languages": ["en"],
  "voices": ["af_heart"]
}
```

This route is optional for minimal sidecars. A 404 means Soulacy assumes both
STT and TTS and lets individual operations report errors.

### `POST /transcribe`

Accept `multipart/form-data` with an `audio` file and return:

```json
{"text":"What is the weather in Chicago?"}
```

### `POST /synthesize`

Accept:

```json
{"text":"It is 72 degrees.","voice":"af_heart"}
```

Return audio bytes with an appropriate `Content-Type`, such as `audio/wav`.

Soulacy limits recorded input to 16 MiB, synthesis text to 32 KiB, and returned
audio to 32 MiB. Remote sidecars are rejected unless `allow_remote: true` is
explicitly configured. Prefer loopback or HTTPS; sidecars never receive LLM
credentials.

## OpenAI realtime (optional)

The original low-latency WebRTC path remains available:

```yaml
voice:
  provider: openai
  model: gpt-realtime-mini
```

It reads the key from `llm.providers.openai.api_key` or `OPENAI_API_KEY`. The
gateway only mints a short-lived browser credential; audio flows directly
between the browser and OpenAI.

## Browser behavior

In sidecar mode Chat runs a continuous turn loop:

- Opening Voice starts microphone capture immediately.
- A short silence ends the user turn and submits it for transcription.
- Microphone capture pauses while the agent is thinking and while speech is
  playing, then resumes automatically after the answer.
- A subtle local tone plays while the LLM is working and stops before response
  playback. Muting voice also silences the waiting tone.
- The user can finish a turn manually, pause or stop playback, or end the voice
  session to stop listening completely.

Each detected turn is transcribed locally, sent through the selected agent's
ordinary chat path, synthesized, and played without requiring another click.

Long replies are synthesized in short sentence groups so playback starts as
soon as the first group is ready. While a response is being prepared or played,
Chat displays **Stop voice** and, during playback, **Pause/Resume** controls.

Microphone capture requires localhost or HTTPS. If voice is not configured,
the microphone remains clickable and opens setup instead of failing silently.

## CLI reference

```bash
sy voice providers
sy voice enable --recipe auto [--no-wait]
sy voice install --recipe auto [--dry-run]
sy voice start --recipe auto
sy voice configure --sidecar-url http://127.0.0.1:8081 [--voice NAME]
sy voice status
sy voice test
sy voice disable
```

Configuration changes require a gateway restart.

`enable` is idempotent: it reuses a compatible managed environment, cached
models, and the existing user service. Use `sy voice install --force` only to
repair or deliberately refresh its Python dependencies. On macOS the background
process is a per-user launch agent; on Linux it is a systemd user service.

## API routes

- `GET /api/v1/voice/status`
- `GET /api/v1/voice/capabilities`
- `POST /api/v1/voice/transcribe`
- `POST /api/v1/voice/synthesize`
- `POST /api/v1/voice/ephemeral` (realtime providers only)

All routes use the Chat RBAC surface.
