#!/usr/bin/env python3
"""Minimal External Channel Protocol sidecar.

Use this as the starting point for Matrix, Signal, SMS, Google Chat, or any
other platform integration. It speaks NDJSON on stdin/stdout and keeps all
platform-specific work isolated behind the protocol functions below.
"""

import json
import sys
import time

PROTOCOL = 1


def emit(frame):
    sys.stdout.write(json.dumps(frame, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def handle_send(frame):
    """Deliver an outbound Soulacy reply to the platform.

    Replace this echo with the platform API call:
      platform.send_message(chat_id=frame["to"], text=frame["text"])
    """
    emit({
        "type": "message",
        "id": "echo-%d" % int(time.time() * 1000),
        "chat_id": frame.get("to", "echo-chat"),
        "sender_id": "echo-user",
        "sender_name": "Echo User",
        "text": "echo: " + frame.get("text", ""),
        "timestamp": int(time.time()),
    })


def main():
    emit({
        "type": "hello",
        "protocol": PROTOCOL,
        "name": "echo-starter",
        "capabilities": ["send"],
    })

    for raw in sys.stdin:
        raw = raw.strip()
        if not raw:
            continue
        try:
            frame = json.loads(raw)
        except json.JSONDecodeError:
            continue

        ftype = frame.get("type")
        if ftype == "hello_ack":
            emit({"type": "status", "connected": True, "detail": "echo starter ready"})
        elif ftype == "send":
            handle_send(frame)
        elif ftype == "shutdown":
            emit({"type": "status", "connected": False, "detail": "shutdown"})
            return
        # Unknown frame types are intentionally ignored for forward compatibility.


if __name__ == "__main__":
    main()
