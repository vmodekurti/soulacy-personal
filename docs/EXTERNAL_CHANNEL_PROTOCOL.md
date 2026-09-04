# External Channel Protocol v1

The External Channel Protocol lets developers add new chat channels to
Soulacy **in any language, without recompiling the gateway** (extensibility
story E3, design: `docs/EXTENSIBILITY.md` §5). A channel is a *sidecar*: a
subprocess spawned by Soulacy that talks to the outside platform (Matrix,
Teams, SMS, …) and exchanges newline-delimited JSON frames with the gateway
over stdin/stdout.

The protocol is a generalisation of the framing proven by the WhatsApp Web
sidecar. Reference implementation: `scripts/reference-channel-sidecar.py`.
Gateway side: `internal/channels/external.Adapter`. Conformance check:
`external.RunConformance` (official dev kit lands in story E11).

## Transport

- One JSON object per line (NDJSON), UTF-8.
- Sidecar stdout → gateway; gateway → sidecar stdin. stderr is captured
  into the gateway log (use it for debugging).
- **Unknown frame types must be ignored by both sides** — this is how the
  protocol grows without breaking existing sidecars.

## Handshake

1. Sidecar starts and MUST send `hello` within **5 seconds**:

   ```json
   {"type":"hello","protocol":1,"name":"matrix","capabilities":["send"]}
   ```

   `protocol` (required, ≥1) is the highest version the sidecar speaks;
   `name` (required) identifies the integration in the GUI.

2. Gateway replies with the negotiated version — `min(gateway, sidecar)`:

   ```json
   {"type":"hello_ack","protocol":1,"shared_dir":"/abs/path/scratch/ch-1a2b3c"}
   ```

   `shared_dir` (optional, Story E24 shared mounts) is the absolute path
   of a per-run scratch directory the gateway provisioned for this
   sidecar. Large attachments move as files under it — referenced by
   relative path — instead of inline frame payloads. Absent/empty = no
   shared dir; sidecars must tolerate both (append-only field rule).

3. From then on both sides speak the negotiated version.

Failure modes: no `hello` in time → process killed, status "handshake
timeout"; `protocol` missing/0 or `name` missing → killed, status
"protocol negotiation failed".

## Frames: sidecar → gateway

| Frame | Purpose | Fields |
|-------|---------|--------|
| `status` | connection state for the Channels GUI | `connected` (bool), `detail` (string), `qr` (optional, pairing flows) |
| `message` | inbound message from the platform | `id`, `chat_id` (required), `sender_id`, `sender_name`, `text` (required), `timestamp` (unix secs), `is_group` |
| `error` | recoverable platform error | `detail` or `error` |

Inbound messages are filtered by the channel's activation policy (trigger
phrase, group/user/thread allowlists) and dispatched to the configured
agent with session id `<channel-id>-<chat_id>` — one conversation per chat.

## Frames: gateway → sidecar

| Frame | Purpose | Fields |
|-------|---------|--------|
| `send` | deliver the agent's reply to the platform | `to` (chat id), `text` |
| `shutdown` | graceful stop; exit promptly | — |

After `shutdown` (or stdin EOF) the sidecar has a 3-second grace period
before SIGKILL.

## Versioning & compatibility

- Integer version, negotiated at handshake; the gateway supports the
  current and previous major version for at least two releases.
- New optional fields and new frame types are **not** version bumps —
  ignore what you don't know.
- Renaming/removing fields or changing semantics bumps the version.

## Conformance checklist

`external.RunConformance(ctx, command, args...)` verifies:

1. `hello` arrives within 5s, with valid `protocol` and `name`
2. negotiation succeeds and the sidecar accepts `hello_ack`
3. unknown frame types from the gateway don't crash it
4. a `send` frame doesn't crash it
5. it exits within 5s of `shutdown`

Run the reference sidecar against it any time:

```
python3 scripts/reference-channel-sidecar.py   # speaks the protocol on stdio
```

## Coming next (roadmap)

- **E4**: supervised lifecycle — health, crash restart with backoff,
  rlimit sandbox baseline.
- **E6**: vault credential delegation — manifest-declared secrets injected
  into the sidecar environment at spawn.
- **E7**: declare sidecar channels in `plugin.yaml` manifest v2 (no core
  config edits).
