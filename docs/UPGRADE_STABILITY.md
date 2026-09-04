# Upgrade Stability & API Compatibility Guards (Story E22)

Three guard layers keep upgrades boring: schema versioning, API contract
tests, and chaos-proven graceful fallbacks.

## 1. Database schema versioning

**Audit result (2026-06-07):** every SQLite store (actionlog, memory
archive, vector store, costs, rbac, api keys, checkpoints, dlq, workboard,
knowledge) boots through idempotent `CREATE TABLE IF NOT EXISTS` schemas —
additive by construction. The only `DROP` in store code is the knowledge
store's per-KB vec-table rebuild, which is a runtime data operation (re-
index), not a schema migration. Plugin schemas go through
`internal/pluginmigrate` (E16: transactional, checksummed, namespaced).

**Adopted (2026-06-07):** every SQLite store now records its schema
version at boot via `sqlitex.RecordSchemaVersion(db, "<component>", 1)`
right after its idempotent bootstrap — components: workboard, costs,
rbac, apikeys, dlq, checkpoints, memory_archive, actionlog, knowledge.
RecordSchemaVersion never downgrades, so databases already migrated past
v1 keep their version.

**Going forward:** stores that need to EVOLVE schema (add columns/tables
after release) use the shared helper in `internal/sqlitex`:

```go
applied, err := sqlitex.MigrateSchema(db, "workboard", []sqlitex.SchemaMigration{
    {Version: 1, SQL: `CREATE TABLE …`},
    {Version: 2, SQL: `ALTER TABLE … ADD COLUMN …`},
})
```

Guarantees: one shared `soulacy_schema_version` table keyed by component
(stores can share a db file); one transaction per step — a failed step
rolls back and the recorded version stays at the last good step; versions
≤ current are skipped, so calling it at every boot is the intended usage;
**additive-only by default** — `DROP`/`RENAME` statements are refused
unless the step sets `Destructive: true` (the explicit deprecation-cycle
opt-in). Pre-upgrade fixtures are covered in
`internal/sqlitex/schemaversion_test.go` (v1 database with data upgrades
to v2, rows intact, v1 never re-applied).

## 2. API contract verification

`internal/gateway/contract_test.go` pins the `/api/v1` response envelopes
the GUI, CLI, and plugins depend on: agents list/detail, config (including
`plugins_config`), providers, skills, the plugin install preview
(staged_id/permissions/fingerprint — the E13/E20 approval-dialog
contract), the `{"error": …}` envelope, and the 401 auth gate. Additive
fields are fine — the tests assert required keys, not exhaustive sets.
Removing or renaming a pinned key fails the suite and demands a
deprecation cycle.

**SDK major gate:** manifests may declare `sdk_major: <n>` (pkg/plugin).
A plugin built against a newer SDK major than the host
(`plugins.CurrentSDKMajor`) is refused at load with an upgrade hint —
extending the E7 `manifest_schema` gate, which still rejects unknown
future manifest grammars.

## 3. Graceful fallbacks (chaos-tested)

A failing plugin never takes down the gateway:

- the loader warns-and-skips broken manifests, future schemas, future SDK
  majors, capability/credential violations, and stale install approvals —
  `internal/plugins/chaos_test.go` proves a healthy plugin loads alongside
  three different casualties;
- every refused plugin is recorded as a `Loader.Diagnostic` and published
  to the event hub at boot (`type: error`, `stage: plugin-load`) so the
  Logs GUI explains every silently absent plugin;
- sidecar crashes are contained by the E4 supervisor (restart backoff;
  the channel reports unhealthy, the gateway keeps serving).
