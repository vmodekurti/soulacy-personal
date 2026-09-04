# Custom Sidecar Channels

Add any chat platform to Soulacy in any language, without recompiling the gateway: a *sidecar* is a subprocess that talks to the platform and exchanges newline-delimited JSON frames with the gateway over stdin/stdout.

## Quick start

A minimal sidecar in any language does three things — say hello, report
status, and relay messages:

```python
#!/usr/bin/env python3
import json, sys

def send(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()

send({"type": "hello", "protocol": 1, "name": "echo", "capabilities": ["send"]})

for line in sys.stdin:
    frame = json.loads(line)
    if frame.get("type") == "hello_ack":
        send({"type": "status", "connected": True, "detail": "ready"})
    elif frame.get("type") == "send":
        pass  # deliver frame["text"] to chat frame["to"] on your platform
    elif frame.get("type") == "shutdown":
        break
    # unknown frame types: ignore — that's how the protocol grows
```

A full reference implementation ships in the repo:
`scripts/reference-channel-sidecar.py`.

For a clone-and-edit starter with a CI-ready conformance test, use
`examples/channel-sidecar-starter/`.

## Transport rules

- One JSON object per line (NDJSON), UTF-8.
- Sidecar stdout → gateway; gateway → sidecar stdin.
- stderr is captured into the gateway log — use it for debugging, never
  for secrets.
- **Unknown frame types must be ignored by both sides.**

## Handshake

1. The sidecar MUST send `hello` within **5 seconds** of starting:

    ```json
    {"type":"hello","protocol":1,"name":"matrix","capabilities":["send"]}
    ```

    `protocol` (required, ≥ 1) is the highest version it speaks; `name`
    (required) identifies the integration in the GUI.

2. The gateway replies with the negotiated version
   (`min(gateway, sidecar)`):

    ```json
    {"type":"hello_ack","protocol":1,"shared_dir":"/abs/path/scratch/ch-1a2b3c"}
    ```

    `shared_dir` (optional) is a per-run scratch directory the gateway
    provisioned for this sidecar: large attachments move as files under
    it — referenced by relative path — instead of inline frame payloads.
    Absent or empty means no shared dir; sidecars must tolerate both.

No `hello` in time → the process is killed with status "handshake
timeout"; missing `protocol` or `name` → "protocol negotiation failed".

## Frames

Sidecar → gateway:

| Frame | Purpose | Fields |
|---|---|---|
| `status` | connection state for the Channels GUI | `connected` (bool), `detail`, `qr` (optional, pairing flows) |
| `message` | inbound message from the platform | `id`, `chat_id` (required), `sender_id`, `sender_name`, `text` (required), `timestamp` (unix secs), `is_group` |
| `error` | recoverable platform error | `detail` or `error` |

Gateway → sidecar:

| Frame | Purpose | Fields |
|---|---|---|
| `send` | deliver the agent's reply | `to` (chat id), `text` |
| `shutdown` | graceful stop — exit promptly | — |

After `shutdown` (or stdin EOF) the sidecar has a 3-second grace period
before SIGKILL. Inbound messages are filtered by the channel's activation
policy and dispatched to the configured agent with session id
`<channel-id>-<chat_id>` — one conversation per chat.

## Supervision

Sidecars declared via a plugin run **supervised**: crashes restart with
backoff, rlimit sandbox limits apply, and the declared vault credentials
are re-resolved and injected into the environment **on every spawn** — a
credential rotation triggers a restart so the sidecar always holds current
values. See [Plugin Security Model](../extend/plugin-security.md).

## Conformance kit

Prove your sidecar against the contract out-of-tree, in CI, with the
official kit (`sdk/extchannel/sidecartest`):

```go
func TestMySidecarConforms(t *testing.T) {
    if err := sidecartest.RunConformance(context.Background(),
        "python3", "my_sidecar.py"); err != nil {
        t.Fatal(err)
    }
}
```

It verifies: `hello` within 5 s with valid `protocol` and `name`;
negotiation succeeds; unknown gateway frames don't crash it; a `send`
frame doesn't crash it; and it exits within 5 s of `shutdown`. The same
kit runs against the reference sidecars in the host's CI, so the contract
and implementations cannot drift.

## Shipping it: declare the channel in a plugin

Users install your channel like any plugin — no core config edits:

```yaml
# plugin.yaml
id: matrix-suite
name: Matrix Suite
version: 1.0.0
manifest_schema: 2

channels:
  - id: matrix
    agent_id: assistant         # required: agent that receives messages
    sidecar:
      command: node             # required
      args: ["sidecar/matrix.mjs"]

permissions:
  - cap: channel.send
    channels: [matrix]

credentials:
  - key: MATRIX_TOKEN
    from: matrix-suite/token    # vault namespace must equal the plugin id
```

!!! warning "Sidecars receive only what they declare"
    The sidecar's environment is built from a minimal allowlist plus
    exactly the declared `credentials:` — the gateway's own environment
    and API keys are never inherited. Never print credentials to stderr:
    it lands in the gateway log.

## Versioning

The protocol version is an integer negotiated at handshake; the gateway
supports the current and previous major version for at least two releases.
New optional fields and new frame types are **not** version bumps — ignore
what you don't know. Renaming/removing fields or changing semantics bumps
the version.

Full specification: [External Channel Protocol](../EXTERNAL_CHANNEL_PROTOCOL.md).
