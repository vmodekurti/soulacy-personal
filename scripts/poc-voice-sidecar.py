#!/usr/bin/env python3
"""Voice bridge sidecar PoC (Story 10 spike — see docs/VOICE_SPIKE.md).

Dependency-free proof that a realtime-voice bridge fits the External
Channel Protocol v1 (docs/EXTERNAL_CHANNEL_PROTOCOL.md) supervised-sidecar
runtime. The provider session is MOCKED — the points where real OpenAI
Realtime / Gemini Live code plugs in are marked with `# REAL:` comments.

What it demonstrates:
  * v1 handshake + status lifecycle (passes the E3 conformance suite);
  * voice-session state surfaced through `status` detail (shows up in the
    Channels GUI with zero new UI work);
  * transcript turns delivered as ordinary `message` frames → the engine
    treats voice exactly like any other channel;
  * the proposed v2 `usage` frame riding over v1 (unknown types are
    ignored by v1 gateways — forward-compat rule).

Run under a manifest-v2 plugin with vault-delegated credentials:

    manifest_schema: 2
    channels:
      - id: voice
        agent_id: my-agent
        sidecar: { command: python3, args: [poc-voice-sidecar.py] }
    credentials:
      - key: OPENAI_API_KEY
        from: voice/openai_api_key
"""

import json
import os
import sys
import time

PROTOCOL = 1


def emit(frame: dict) -> None:
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def main() -> None:
    emit({
        "type": "hello",
        "protocol": PROTOCOL,
        "name": "voice-bridge-poc",
        "capabilities": ["send", "status"],
    })

    turn = 0
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            frame = json.loads(line)
        except json.JSONDecodeError:
            continue

        ftype = frame.get("type", "")

        if ftype == "hello_ack":
            # REAL: validate OPENAI_API_KEY / GEMINI_API_KEY from the
            # vault-delegated environment (E6) and open the provider
            # session (WebSocket) or prepare ephemeral-key minting for
            # browser WebRTC.
            has_key = bool(os.environ.get("OPENAI_API_KEY") or os.environ.get("GEMINI_API_KEY"))
            detail = ("voice bridge ready (provider session mocked; "
                      + ("credentials delegated" if has_key else "no provider key — setup required")
                      + ")")
            emit({"type": "status", "connected": True, "detail": detail})

        elif ftype == "send":
            # REAL: forward the utterance/control message into the live
            # provider session and stream transcript deltas back. Here we
            # mock one full assistant turn.
            turn += 1
            chat = frame.get("to", "voice-session")
            emit({"type": "message",
                  "id": f"voice-{turn}",
                  "chat_id": chat,
                  "sender_id": "voice-user",
                  "sender_name": "Voice User",
                  "text": f"[transcript] {frame.get('text', '')}",
                  "ts": int(time.time())})
            # Proposed protocol-v2 usage frame (ignored by v1 gateways):
            emit({"type": "usage",
                  "input_tokens": 320, "output_tokens": 250,
                  "modalities": {"audio_in": 320, "audio_out": 250,
                                 "text_in": 0, "text_out": 0}})

        elif ftype == "shutdown":
            emit({"type": "status", "connected": False, "detail": "voice bridge stopped"})
            return
        # Unknown frame types: ignored (forward compatibility).


if __name__ == "__main__":
    main()
