# Audit & incident-reconstruction records

Soulacy keeps two separate records of agent activity. They serve different
purposes and have very different guarantees. **Know which one is authoritative.**

## The authoritative record: SQLite action log

`internal/actionlog` is **THE authoritative incident-reconstruction record.**

- Backed by **SQLite** (`agent_events` table) plus a mirrored per-agent
  `<agent-id>.log` JSON-Lines file that the GUI Logs page tails.
- **Always on.** It is part of the core storage backend
  (`storage.backend: sqlite` by default; also available via Postgres) and is
  wired unconditionally at startup — there is no config switch to turn it off.
- **Durable and queryable.** Survives restarts, supports cross-agent queries,
  and is written by a single buffered async writer that batches and fsyncs.
- Records the full lifecycle of every run: run start, LLM calls, tool
  calls/results, replies, and errors, flowing through the gateway EventHub.

When you need to reconstruct what an agent did — for an incident review,
forensic timeline, or audit — **this is the source of truth.**

Location: `<workspace>/logs/` for the per-agent JSONL mirror and
`<workspace>/data/actions.db` (SQLite) for the durable store. With
`storage.backend: postgres`, the durable store lives in Postgres instead.

## The optional record: JSONL audit log

`internal/audit` writes an optional per-session JSONL tool-call log.

- **Optional debug output. OFF by default.** Controlled by
  `runtime.audit_dir`, which now **defaults to empty (disabled)**. When empty,
  the audit logger is a no-op.
- **Redundant.** It captures a subset of what the action log already records
  (tool name, redacted args, result length, duration, denied/error flags). It
  exists as a convenience tail for quick `grep`/`tail -f` inspection during
  development, not as a system of record.
- **Best-effort.** Write failures are swallowed silently so they never crash an
  agent run — which is exactly why it must not be relied upon for incident
  reconstruction.

To enable it (for example during debugging), set an explicit directory:

```yaml
runtime:
  audit_dir: /Users/yourname/.soulacy/audit
```

Each session then gets `<audit_dir>/<YYYY-MM-DD>/<sessionID>.jsonl`. Secret-
looking argument values (`api_key`, `password`, `secret`, `token`,
`credential`, `auth`) are redacted before they hit disk.

## Summary

| | SQLite action log (`internal/actionlog`) | JSONL audit log (`internal/audit`) |
|---|---|---|
| Authority | **Authoritative** — system of record | Optional convenience copy |
| Default state | Always on (cannot be disabled) | **Off** (`audit_dir: ""`) |
| Storage | SQLite (+ GUI JSONL mirror) / Postgres | Per-session JSONL files |
| Durability | Durable, queryable, async-batched | Best-effort, failures swallowed |
| Use for incident reconstruction | **Yes** | No — use the action log |
