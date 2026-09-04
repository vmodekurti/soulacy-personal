# Dashboard & Activity

The Dashboard streams every runtime event live as it happens, and the Activity page lets you replay any agent's full action history after the fact.

## Quick start

1. Open **Dashboard** (`http://localhost:18789` → ◈ Dashboard).
2. Trigger anything — send a chat, run a scheduled agent — and watch events scroll in under **Live Event Log**.
3. Switch to **📈 Activity**, pick the agent, and see the same run as a structured, persistent log.

CLI/API equivalents:

```bash
# Persistent per-agent action history (same data as the Activity page)
curl "http://localhost:18789/api/v1/agents/<agent-id>/actions?limit=500" \
  -H "Authorization: Bearer $SOULACY_API_KEY"

# Per-session run metrics (tokens, cost, LLM calls)
curl "http://localhost:18789/api/v1/runs/<session-id>/metrics" \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

## Dashboard

Three status cards up top:

- **Gateway** — online/offline and version (from the unauthenticated `/health` endpoint).
- **Agents** — total loaded and how many are enabled.
- **Events (session)** — how many events this browser tab has received, and whether the stream is live.

Below them, the **Live Event Log** shows every event as it is published, newest first (last 300 kept). Filter presets narrow the view without dropping the buffer:

| Filter | Shows |
|---|---|
| All | Everything |
| Errors | `error` events and payloads containing "error" |
| Tools | `tool.call`, `tool.result`, … |
| LLM | `llm.call`, `llm.result` |
| Messages | `message.in`, `message.out` |

!!! note
    The Dashboard log is a live view only — it starts empty on page load and is cleared with the **Clear** button. For history, use Activity.

## Activity

Activity reads the durable action log (SQLite), so it survives restarts and shows complete past runs. Pick an agent, then:

- **▶ Watch (2s)** polls continuously — handy while a long run is executing (the Schedule page's **👁 Watch** button deep-links here with watching already on).
- Filter chips: `all`, `run` (message in/out), `llm`, `tools`, `errors`.
- After each completed run (`message.out` or `error`), a **Σ run** row shows that session's token and cost totals.
- The source bar shows which log file backs the view and how many events match.

## What the event types mean

Every notable action becomes a typed event (full schema in `docs/EVENTS.md`):

| Type | Meaning for you |
|---|---|
| `message.in` | A run started — an inbound message, cron fire, or manual trigger reached the agent |
| `message.out` | The agent replied; the payload is the full reply |
| `llm.call` / `llm.result` | One model turn: which model, tokens in/out, duration, and how many tool calls it requested |
| `tool.call` | The LLM asked to run a tool — name and arguments |
| `tool.result` | The tool returned — content plus an `is_error` flag (the Schedule history uses this to mark runs failed even when the LLM still produced a reply) |
| `reasoning.start` / `reasoning.step` / `reasoning.result` | A multi-step reasoning loop: strategy and limits at start, each step's thought and chosen tool, then total steps, confidence, and duration |
| `run.started` / `run.finished` / `run.failed` | Workboard task run lifecycle (task ID, run ID, attempt, failure reason) |
| `run.artifact` | A run produced a file — path, size, and which tool wrote it; it appears in the task's Artifacts panel |
| `rulebook.updated` | A self-updating agent rewrote its procedural rulebook (only with `auto_update: true`); review it on the Brain Mem page |
| `error` | A run errored — the payload names the failing stage |

Reading a healthy run: `message.in` → (`llm.call` → `tool.call` → `tool.result`)× → `llm.result` → `message.out`. Anything red is an `error` or a `tool.result` with `is_error: true`.

## Beyond the GUI

The same events feed external consumers:

- **Queue subjects** — `soulacy.events.<type>` on the configured queue backend (`queue.backend: nats` for cross-process consumers; wildcards like `soulacy.events.run.*` work).
- **Webhooks** — declare `hooks:` in `config.yaml` to have matching envelopes POSTed to your endpoint with an HMAC signature.

!!! warning
    Event delivery is best-effort and non-blocking. Anything you must not lose (auditing, billing) should read the durable stores — the action log and costs DB — not the live stream.
