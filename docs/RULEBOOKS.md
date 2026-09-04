# Versioned Agent Rulebooks (Story E23, audit response)

Self-updating agents are powerful and dangerous: an agent with
`brain_memory.procedural.auto_update: true` rewrites its own operating
rules after each reasoning run. Before E23 those rewrites overwrote
`procedural.md` with no history — behavioural drift was invisible and
irreversible, and Reflect's `updated_rules` was actually being **dropped**
before ever persisting. E23 fixes both with GitOps-style versioning.

## How it works

- **Every rule write is an immutable version.** The serving copy stays
  `<memory>/<agent>/procedural.md` (all read paths unchanged); each write
  also appends to `rulebook_versions` in `<memory>/rulebook.db`
  (sqlitex-managed schema) with provenance: `auto_update` (the reasoning
  loop), `manual` (GUI PUT), or `rollback`.
- **The loop now persists learning — gated.** `Loop.Run` surfaces
  `UpdatedRules`; the engine writes it ONLY when the agent opted in via
  `auto_update: true`, emitting a `rulebook.updated` event (visible in
  Activity). No opt-in → discarded, exactly as before.
- **Locks freeze rules entirely.** A locked agent refuses auto-updates
  AND manual writes (HTTP 423); the reasoning run itself still succeeds,
  with a warn event recording the refused write. Rollback also requires
  unlocking — locking means *nothing changes these rules*.
- **Rollback never rewrites history.** Rolling back to v3 re-applies v3's
  text as a NEW version with source `rollback`, so the audit trail stays
  append-only.

## API

```
GET  /api/v1/brain-memory/:agent/rulebook            → {current, locked, versions[]}
GET  /api/v1/brain-memory/:agent/rulebook/:version   → {version, rules}
POST /api/v1/brain-memory/:agent/rulebook/rollback   {"version": 3}
POST /api/v1/brain-memory/:agent/rulebook/lock       {"locked": true}
```

## GUI

Brain Mem → Procedural tab: a **Lock** toggle (🔒 freezes the rulebook
with an explanatory banner), and a **History** panel listing every version
with provenance badge, timestamp, and size — each row offers *Diff vs
current* (line-level add/remove view) and one-click *Roll back*.

## Degradation

If `rulebook.db` cannot open, rule writes degrade to unversioned (the
agent keeps working; history reads report empty) — versioning never blocks
boot.
