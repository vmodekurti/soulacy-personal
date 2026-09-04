# Soulacy SDK

`github.com/soulacy/soulacy/sdk` — the stable, semver-versioned contracts
for building Soulacy extensions in Go (Story E9, `docs/EXTENSIBILITY.md` §6).

This module is intentionally tiny and dependency-free (stdlib only). It
versions independently of the soulacy application: extension authors depend
on the SDK, never on `internal/` packages.

## Packages

| Package | Contract |
|---|---|
| `sdk/message` | Canonical message, event, tool-call, and attachment types |
| `sdk/channel` | `channel.Adapter` — messaging-platform bridges |
| `sdk/llm` | `llm.Provider` + completion request/response types |
| `sdk/queue` | `queue.Backend` — durable message queue implementations |
| `sdk/vector` | `vector.Backend` — semantic search stores |
| `sdk/memory` | Memory `Entry`/`Scope` shared by vector + storage |
| `sdk/storage` | `storage.ActionLogBackend`, `storage.MemoryBackend` |
| `sdk/registry` | Named factory registries — drivers self-register from `init()` |
| `sdk/reasoning` | `reasoning.Strategy` — pluggable reasoning loops (E15) |
| `sdk/extchannel` | External Channel Protocol v1 wire types (NDJSON frames) |
| `sdk/extchannel/sidecartest` | Sidecar protocol conformance runner (E11) |
| `sdk/extstorage` | External Storage Protocol v1 — JSON-RPC over stdio for vector/queue sidecars (E24) |
| `sdk/extstorage/storagetest` | Storage sidecar conformance runner (E24) |
| `sdk/channel/channeltest` | `RunAdapterSuite` — channel adapter conformance kit (E11) |
| `sdk/llm/providertest` | `RunProviderSuite` — LLM provider conformance kit (E11) |

The application re-exports all of these as type aliases at the historical
paths (`pkg/message`, `internal/channels`, `internal/llm`, `internal/queue`,
`internal/vector`, `internal/storage`, `internal/memory`), so SDK types and
app types are identical — not merely convertible.

## Compatibility policy

1. **Semver.** Breaking changes only with a new major version. v0.x is
   pre-stabilisation: minor versions may break, patch versions never do.
2. **Interfaces are frozen within a major version.** We NEVER add methods to
   an exported interface — that breaks every out-of-tree implementation.
   Additive capability arrives as an *extension interface* the host
   discovers by type assertion:

   ```go
   // in a later minor version
   type AdapterDoner interface{ Done() <-chan struct{} }
   // host side
   if d, ok := adapter.(AdapterDoner); ok { … }
   ```

3. **Structs grow by appending fields.** Zero values of new fields must
   preserve the old behaviour. Field renames/removals are major-version
   events.
4. **Constants and enum-like values are append-only** and never change
   meaning (`memory.ScopeSession` stays `"session"` forever).
5. **Wire formats carry explicit versions** (event `schema`, External
   Channel Protocol `protocol`, plugin `manifest_schema`); the SDK types
   mirror, never define, those versions.
6. **No dependencies.** The SDK stays stdlib-only so its semver is never
   hostage to a third-party module.

## Conformance kits

Exported test suites for extension authors (`channeltest.RunAdapterSuite`,
`providertest.RunProviderSuite`, sidecar conformance) arrive with Story E11.
Until then, see `internal/channels/external/conformance.go` for the sidecar
suite shape.
