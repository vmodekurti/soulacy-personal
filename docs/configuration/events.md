# Events & Webhooks

Every notable action inside Soulacy — messages, tool calls, workboard
runs, reasoning steps — is published as a versioned JSON envelope. Hook
your dashboards, alerting, and log shippers into the stream instead of
polling the API.

Three ways to consume events:

1. **Signed webhooks** (`hooks:` in config.yaml) — easiest, works with the
   default in-process queue.
2. **WebSocket** `/ws/events` — what the GUI uses; great for live tooling.
3. **NATS subjects** — set `queue.backend: nats` and subscribe from any
   process or machine.

## The envelope (schema v1)

```json
{
  "schema": 1,
  "id": "9b2f6c1e-…",
  "type": "run.failed",
  "agent_id": "support-bot",
  "session_id": "wb-12-1765043210",
  "ts": "2026-06-06T21:04:05Z",
  "data": { "task_id": 12, "run_id": 3, "attempt": 2, "failure_reason": "…" }
}
```

`data` carries the type-specific payload verbatim — an object, a string,
or null. New event types and fields may be added at any time; consumers
**must ignore unknown types and fields**. Renaming or removing a field
bumps `schema`, with the previous schema dual-published for at least two
releases. Full contract: [`docs/EVENTS.md`](../EVENTS.md).

## Event types

| Type | Emitted when | `data` highlights |
|------|--------------|-------------------|
| `message.in` | a run starts (inbound message) | full inbound message |
| `message.out` | the agent replies | full reply message |
| `tool.call` | the LLM requests a tool | `name`, `arguments` |
| `tool.result` | a tool returns | `name`, `content`, `is_error` |
| `llm.result` | one LLM turn completes | `model`, `input_tokens`, `output_tokens`, `duration_ms`, `tool_calls` |
| `error` | a run errors (incl. plugin load failures at boot, `stage: plugin-load`) | `stage`, `error` |
| `run.started` | a Workboard run begins | `task_id`, `task_title`, `run_id`, `attempt` |
| `run.finished` | a Workboard run succeeds | same as `run.started` |
| `run.failed` | a Workboard run fails | + `failure_reason` |
| `run.artifact` | a run produced a file | + `path`, `size_bytes`, `tool` |
| `reasoning.start` | a reasoning strategy begins | strategy details |
| `reasoning.step` | one reasoning step completes | step details |
| `reasoning.result` | the strategy concludes | final result |
| `rulebook.updated` | an agent's procedural rulebook gains a new version | version info |

## Queue subjects

Envelopes are published on the configured queue backend
([`queue:` config](storage.md#message-queue-queue)) under:

```
soulacy.events.<type>        e.g. soulacy.events.run.failed
```

Subscribe with NATS wildcards:

- `soulacy.events.>` — everything
- `soulacy.events.run.*` — workboard run lifecycle only
- `soulacy.events.tool.*` — tool activity only

With the default `memory` backend, subjects exist in-process only (used
by the webhook dispatcher). Set `queue.backend: nats` to consume events
externally.

## Signed outbound webhooks (`hooks:`)

Declare endpoints in `config.yaml`; the dispatcher consumes the queue and
POSTs matching envelopes:

```yaml
hooks:
  - on: [run.failed, "tool.*"]       # exact, "x.*" prefix, or "*"
    agents: [support-bot]             # optional agent filter; empty = all
    url: https://ops.example.com/soulacy
    secret_env: SOULACY_HOOK_SECRET   # env var holding the HMAC secret
```

| Key | Description |
|-----|-------------|
| `on` | Event types to deliver: exact (`run.failed`), prefix (`tool.*`), or `*` for all |
| `agents` | Only deliver events from these agents (empty = all agents) |
| `url` | Endpoint that receives `POST` with the envelope as JSON body |
| `secret_env` | Name of the env var holding the HMAC signing secret |

Each delivery carries these headers:

```
X-Soulacy-Event:     run.failed         # envelope type
X-Soulacy-Delivery:  <envelope id>      # unique per emission
X-Soulacy-Signature: t=1765043210,v1=<hex hmac-sha256>
```

The signature is HMAC-SHA256 over `"<t>.<raw body>"` with your secret.
Verify by recomputing and comparing in constant time, and reject
timestamps older than 5 minutes (replay guard). Reference implementation:
`hooks.VerifySignature` in `internal/hooks`.

**Retries:** failed deliveries (non-2xx or network error) retry up to 5
times with exponential backoff + jitter (1s → 10m cap). After exhaustion
the delivery is dropped and a `webhook.dead` warning is logged.

!!! warning "Best-effort, not at-least-once"
    Publishing never blocks the engine. If the internal buffer fills
    (broker down), events are dropped with a warning. Anything that must
    not be lost should read the durable stores (action log, costs DB)
    rather than the event stream.

## WebSocket stream (`/ws/events`)

The same events the GUI renders, available to your own tools:

```bash
# wscat example — browser WebSockets can't set headers, so the
# credential may be passed as a query parameter:
wscat -c "ws://localhost:18789/ws/events?api_key=sy_..."
```

Authentication uses the same engine as the REST API. Scoped plugin tokens
are accepted only when the plugin's manifest grants the
`events.subscribe` capability; plugin tokens without that grant get 403.

Broadcasting is non-blocking: a slow client's queue fills and events are
dropped for that client rather than stalling the agent engine.

## See also

- [`docs/EVENTS.md`](../EVENTS.md) — the full schema contract
- [Storage & backends](storage.md) — queue backend configuration
- [API overview](../api/index.md) — REST routes including `/ws/events`
