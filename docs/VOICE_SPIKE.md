# Realtime Voice Spike (Story 10)

Status: spike complete, 2026-06-06 · Decision: **sidecar bridge, OpenAI
Realtime first, Gemini Live second** · PoC: `scripts/poc-voice-sidecar.py`

This spike compares the two realtime voice integration paths for Soulacy
Chat and evaluates delivering the bridge as a supervised stdio sidecar
(External Channel Protocol, E3/E4) instead of linking vendor SDKs into the
core binary. **No product integration is committed here** — Story 11 builds
the MVP on this plan.

## 1. Provider comparison (verified June 2026)

| | OpenAI Realtime | Gemini Live |
|---|---|---|
| Models | `gpt-realtime`, `gpt-realtime-mini` | Gemini Live native-audio models |
| Transport | **WebRTC (recommended for browsers)**, WebSocket (server pipelines), SIP | **WebSocket only** |
| Browser connect | Ephemeral client token from our server → browser opens WebRTC directly to OpenAI | Ephemeral tokens (`v1alpha` endpoint, `access_token` query param) → browser WS, or proxy through our server |
| Audio framing | Opus/PCM over WebRTC tracks; PCM16 base64 events over WS | PCM16 base64 over WS; ~32 tok/s audio in, ~25 tok/s audio out |
| Interruption | Server VAD with `response.cancel` + truncation events; barge-in well supported | Activity events (`activityStart/End`), automatic VAD; interruption supported |
| Auth | Standard API key server-side; ephemeral key minting for clients | API key server-side; ephemeral tokens for direct client connect |
| Cost (list, Jun 2026) | gpt-realtime: $4/$16 per 1M text in/out, $32/$64 per 1M audio in/out (≈$0.18–0.24/min); mini ≈ $0.06–0.10/min | Live API ≈ $1/1M input tokens; roughly an order of magnitude cheaper per minute than Realtime mini at scale |
| Maturity | GA, SIP + phone integrations, broad ecosystem | GA on Vertex/AI Studio; thinner ecosystem |

Sources: [OpenAI Realtime WebRTC guide](https://developers.openai.com/api/docs/guides/realtime-webrtc),
[OpenAI pricing](https://developers.openai.com/api/docs/pricing),
[Realtime cost management](https://developers.openai.com/api/docs/guides/realtime-costs),
[Gemini Live API overview](https://ai.google.dev/gemini-api/docs/live-api),
[Gemini Live WebSocket quickstart](https://ai.google.dev/gemini-api/docs/live-api/get-started-websocket),
[Gemini pricing](https://ai.google.dev/gemini-api/docs/pricing).

## 2. Architecture decision: supervised sidecar bridge

Evaluated against linking SDKs into the Go binary:

- **Single static binary preserved.** Realtime stacks drag in WebRTC/audio
  codec dependencies (libopus, DTLS, vendor SDKs that move monthly). The
  sidecar keeps all of that out of core — the E14 WASM argument all over.
- **The E3–E6 runtime already gives us everything the bridge needs:**
  supervised crash/backoff restarts (E4), rlimit sandboxing (E4), vault
  credential delegation with rotation→restart (E6: `OPENAI_API_KEY` /
  `GEMINI_API_KEY` declared in a manifest, never in core config), and
  manifest v2 packaging (E7) so a voice bridge installs like any plugin.
- **Latency is acceptable.** The browser captures mic audio and talks to
  the provider **directly** (WebRTC w/ ephemeral key for OpenAI; WS
  ephemeral token for Gemini). The sidecar's job is *session control*, not
  audio relay: mint ephemeral credentials, configure the session (agent
  system prompt, tools), receive transcripts, and forward them into the
  engine as messages. Audio never transits the gateway → no added latency.
- **Cost tracking** flows through the existing per-session metrics: the
  sidecar reports usage (tokens by modality) via `status`-frame details
  today; a `usage` frame is the natural protocol v2 extension feeding
  `internal/costs` (same path Story 7 built).

Verdict: **sidecar confirmed.** Protocol v1 suffices for the MVP control
plane; two additive frame types are proposed for v2 (see §4).

## 3. MVP shape (input to Story 11)

1. **Voice panel in Chat** (push-to-talk): mic permission flow → capture →
   browser connects WebRTC to OpenAI with an ephemeral key minted by the
   voice sidecar (requested via gateway → sidecar `send` frame) →
   transcript deltas render live; final user/assistant turns post into the
   normal chat session (`message` frames → engine).
2. **Provider config**: a `voice` plugin (manifest v2) declares the sidecar
   plus `credentials: [{key: OPENAI_API_KEY, from: voice/openai_api_key}]`.
   No realtime provider configured → the panel shows a setup hint and
   stays disabled (graceful fallback).
3. **Cost/status indicators**: session state from `AdapterStatus` (already
   surfaced in the Channels GUI); per-turn token deltas reuse Story 9's
   chat metrics strip once usage flows into `internal/costs`.
4. **Interruption**: handled provider-side (server VAD / `response.cancel`)
   in the browser↔provider leg; the sidecar only records the truncated
   transcript.
5. Gemini Live lands second behind the same sidecar contract (different
   ephemeral-token dance, WS instead of WebRTC) — switching is a manifest
   change, not a core change.

## 4. Proposed protocol v2 extensions (additive, not required for MVP)

```
sidecar → gateway:
  {"type":"usage","input_tokens":N,"output_tokens":N,
   "modalities":{"audio_in":N,"audio_out":N,"text_in":N,"text_out":N}}
  {"type":"ephemeral_key","key":"…","expires_at":…}   // reply to a request
gateway → sidecar:
  {"type":"ephemeral_key_request","session":"…"}
```

Unknown frame types are already ignored by both sides (E3 forward-compat
rule), so v1 gateways and v2 sidecars interoperate.

## 5. Proof of concept

`scripts/poc-voice-sidecar.py` — dependency-free Python sidecar that:

- passes the E3 conformance suite (`RunConformance`) — verified in-sandbox;
- mocks the provider session lifecycle (`connecting → connected`,
  voice-session status details);
- answers `send` frames with a mock transcript turn and a `usage` frame
  (demonstrating the v2 extension riding over v1 without breaking it);
- documents where the real OpenAI/Gemini session code plugs in.

## 6. Risks / open questions for Story 11

- Ephemeral-key minting requires outbound HTTPS from the sidecar — covered
  by credentials delegation, but the sandbox rlimit profile must allow
  network (it does; it limits CPU/mem/files only).
- Browser WebRTC needs HTTPS (or localhost) for `getUserMedia` — fine for
  the default local deployment; document for remote setups.
- Audio transcripts in chat history: store text only (no audio blobs) for
  the MVP; revisit retention later.
- Per-agent voice enablement: gate the panel on agent config rather than
  global config so costs stay opt-in.
