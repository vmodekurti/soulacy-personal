# Soulacy Event Stream (schema v1)

Every notable action inside Soulacy is published as a versioned JSON
envelope to the configured queue backend (story E1) and delivered to
configured webhooks (story E2). External tools — dashboards, alerting,
log shippers — subscribe to these instead of polling the API.

## Envelope

```json
{
  "schema": 1,
  "id": "9b2f6c1e-…",                  // unique per emission (UUID v4)
  "type": "run.failed",
  "agent_id": "support-bot",
  "session_id": "wb-12-1765043210",
  "ts": "2026-06-06T21:04:05Z",
  "data": { "task_id": 12, "run_id": 3, "attempt": 2, "failure_reason": "…" }
}
```

`data` carries the type-specific payload (the engine's event payload,
verbatim). It may be an object, a string, or null.

## Event types

| Type | Emitted when | data highlights |
|------|--------------|-----------------|
| `message.in` | a run starts (inbound message) | full inbound message |
| `message.out` | the agent replies | full reply message |
| `tool.call` | the LLM requests a tool | `name`, `arguments` |
| `tool.result` | a tool returns | `name`, `content`, `is_error` |
| `error` | a run errors | `stage`, `error` |
| `llm.result` | one LLM turn completes | `model`, `input_tokens`, `output_tokens`, `duration_ms`, `tool_calls` |
| `run.started` | a Workboard run begins | `task_id`, `task_title`, `run_id`, `attempt` |
| `run.finished` | a Workboard run succeeds | same as run.started |
| `run.failed` | a Workboard run fails | + `failure_reason` |
| `run.artifact` | a run produced a file (Story 13) | + `path`, `size_bytes`, `tool` |

New types may be added at any time; consumers must ignore unknown types.

## Queue subjects

Envelopes are published to the queue backend (memory or NATS, per
`queue.backend` in config.yaml) on:

```
soulacy.events.<type>        e.g. soulacy.events.run.failed
```

Subscribe with NATS wildcards:

- `soulacy.events.>` — everything
- `soulacy.events.run.*` — workboard run lifecycle only
- `soulacy.events.tool.*` — tool activity only

With the default `memory` backend, subjects exist in-process only (used by
the webhook dispatcher). Configure `queue.backend: nats` to consume events
from other processes or machines.

## Compatibility rules

- `schema` stays `1` while changes are **additive** (new fields, new event
  types). Consumers must tolerate unknown fields and unknown types.
- Renaming, removing, or retyping an existing field bumps `schema`; the
  previous major schema keeps being emitted for at least two releases
  (dual-publish) before removal.
- Subject layout (`soulacy.events.<type>`) is part of the contract.

## Outbound webhooks (story E2)

Declare endpoints in `config.yaml`; the dispatcher consumes the queue and
POSTs matching envelopes:

```yaml
hooks:
  - on: [run.failed, "tool.*"]       # exact, "x.*" prefix, or "*"
    agents: [support-bot]             # optional; empty = all agents
    url: https://ops.example.com/soulacy
    secret_env: SOULACY_HOOK_SECRET   # env var holding the HMAC secret
```

Each delivery is `POST <url>` with `Content-Type: application/json`, the
envelope as the body, and headers:

```
X-Soulacy-Event:     run.failed         # envelope type
X-Soulacy-Delivery:  <envelope id>      # unique per emission
X-Soulacy-Signature: t=1765043210,v1=<hex hmac-sha256>
```

The signature is HMAC-SHA256 over `"<t>.<raw body>"` with your secret.
Verify by recomputing and comparing in constant time; reject timestamps
older than 5 minutes (replay guard). Reference implementation:
`hooks.VerifySignature` in `internal/hooks`.

Retries: failed deliveries (non-2xx or network error) retry up to 5 times
with exponential backoff + jitter (1s → 10m cap). After exhaustion the
delivery is dropped and a `webhook.dead` warning is logged with the hook
URL, event ID, and reason. Delivery is therefore **best-effort, not
at-least-once** — see below.

## Delivery semantics

Publishing is **best-effort and non-blocking**: the engine never waits on
the broker, and if the internal buffer fills (broker down) events are
dropped with a warning log. Anything that must not be lost should read the
durable stores (action log SQLite, costs DB) rather than the event stream.
