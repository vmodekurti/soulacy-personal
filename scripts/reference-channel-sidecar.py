#!/usr/bin/env python3
"""Reference External Channel Protocol v1 sidecar (story E3).

A minimal, dependency-free implementation of the sidecar side of
docs/EXTERNAL_CHANNEL_PROTOCOL.md. It "connects" to a fake platform and
echoes every outbound send back as an inbound message — useful as a
starting point for real integrations (Matrix, Teams, SMS, …) and for
manual end-to-end testing:

    channels:
      echo:
        kind: external          # (wired in story E7's manifest v2)
        command: python3
        args: [scripts/reference-channel-sidecar.py]
        agent_id: my-agent

Protocol rules demonstrated here:
  * print exactly one JSON object per line on stdout (NDJSON)
  * send `hello` first, then wait for `hello_ack`
  * ignore frame types you don't recognise (forward compatibility)
  * exit promptly on `shutdown` or stdin EOF
"""

import json
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
        "name": "reference-echo",
        "capabilities": ["send"],
    })

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
            # Handshake complete — pretend we connected to a platform.
            emit({"type": "status", "connected": True,
                  "detail": "reference echo sidecar ready"})
            emit({"type": "message", "id": "ref-1", "chat_id": "echo-chat",
                  "sender_id": "ref-user", "sender_name": "Reference User",
                  "text": "sidecar online — say something and I will echo it",
                  "timestamp": int(time.time())})

        elif ftype == "send":
            # Echo the outbound text back as a new inbound message.
            emit({"type": "message", "id": f"ref-echo-{int(time.time()*1000)}",
                  "chat_id": frame.get("to", "echo-chat"),
                  "sender_id": "ref-user", "sender_name": "Reference User",
                  "text": "echo: " + frame.get("text", ""),
                  "timestamp": int(time.time())})

        elif ftype == "shutdown":
            emit({"type": "status", "connected": False, "detail": "shutting down"})
            return

        # Unknown frame types: ignored, per protocol.


if __name__ == "__main__":
    main()
