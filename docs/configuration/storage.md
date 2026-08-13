# Storage & Backends

Soulacy persists everything locally by default — zero external
dependencies. When you outgrow a single node, each layer (durable storage,
vector search, message queue) can be swapped independently via
`config.yaml`, including out-of-process **sidecar** backends that speak the
External Storage Protocol.

```yaml
storage:
  backend: sqlite        # default — nothing else needed
```

## Durable storage (`storage:`)

The durable event-log and memory-archive backend.

| Key | Default | Description |
|-----|---------|-------------|
| `backend` | `sqlite` | `sqlite`, `postgres`, or `external` |
| `postgres_dsn` | — | libpq connection string (postgres only) |
| `postgres_log_dir` | `memory.dir` | Directory for per-agent `.log` mirror files (postgres only) |
| `command` | — | Sidecar executable (external only) |
| `args` | — | Sidecar arguments (external only) |

### SQLite (default)

Embedded, zero-dependency, ideal for single-node deployments. Databases
live under the workspace `data/` directory (see
[Workspace Layout](workspace.md)) — `actions.db`, `archive.db`,
`knowledge.db`, `costs.db`, `workboard.db`, and friends.

### PostgreSQL

```yaml
storage:
  backend: postgres
  postgres_dsn: "postgres://user:pass@host:5432/soulacy?sslmode=disable"
  # postgres_log_dir: /var/lib/soulacy/logs   # optional .log mirrors
```

## Vector search (`vector:`)

Semantic memory search. When `vector.backend` and the legacy
`memory.vector_db` setting are both empty, runtime selects the built-in
`sqlite-vec` backend. This keeps semantic agent memory persistent by default.

| Key | Default | Description |
|-----|---------|-------------|
| `backend` | `sqlite-vec` (effective runtime default) | `sqlite-vec`, `qdrant`, or `external` |
| `url` | — | Qdrant base URL, e.g. `http://localhost:6333` |
| `collection` | — | Qdrant collection name, e.g. `soulacy_memory` |
| `api_key` | — | Qdrant API key (optional) |
| `dims` | `768` | Embedding dimensionality — must match your embedder |
| `command` / `args` | — | Sidecar process (external only) |

```yaml
# Built-in and recommended default
vector:
  backend: sqlite-vec

# Qdrant
vector:
  backend: qdrant
  url: http://localhost:6333
  collection: soulacy_memory
  dims: 768
```

!!! warning "Qdrant is experimental"
    The `qdrant` vector backend has **no automated tests and no known
    production users**. Enabling it logs a startup WARN. It is unsupported —
    prefer `sqlite-vec` (the default) or an external sidecar for anything you
    depend on.

## Message queue (`queue:`)

Carries the [event stream](events.md) and internal work distribution.

| Key | Default | Description |
|-----|---------|-------------|
| `backend` | `memory` | `memory`, `nats`, or `external` |
| `nats_url` | `nats://localhost:4222` | NATS server URL; comma-separated list for clusters |
| `nats_stream` | `soulacy` | JetStream stream that owns Soulacy subjects |
| `nats_subject_prefix` | `""` (= `<stream>.>`) | Subject filter applied to the stream |
| `nats_ack_wait` | `30s` | How long JetStream waits for an Ack before redelivering |
| `nats_max_deliver` | `0` | Max delivery attempts per message; `0` = unlimited |
| `command` / `args` | — | Sidecar process (external only) |

```yaml
queue:
  backend: nats
  nats_url: nats://localhost:4222
  nats_stream: soulacy
  nats_ack_wait: 30s
```

With the default `memory` backend, events exist in-process only (still
consumed by the webhook dispatcher). Switch to `nats` to consume events
from other processes or machines.

!!! warning "NATS is experimental"
    The `nats` queue backend has **no automated tests and no known production
    users**. Enabling it logs a startup WARN. It is unsupported — the default
    in-memory queue is the supported path; use an external sidecar if you need
    cross-process durability.

## External sidecars (External Storage Protocol)

Vector and queue backends can be served by a **sidecar process** speaking
the External Storage Protocol — JSON-RPC 2.0 over stdio — so third-party
database drivers plug in at runtime in any language, without recompiling
Soulacy. The gateway spawns the sidecar, negotiates capabilities, and
provisions a per-run shared scratch directory (`data/scratch/…`) so large
payloads move as files instead of stdio JSON. Full wire spec and
conformance kit (`sdk/extstorage/storagetest`): see
[`docs/EXTERNAL_STORAGE_PROTOCOL.md`](../EXTERNAL_STORAGE_PROTOCOL.md).

```yaml
vector:
  backend: external
  command: /usr/local/bin/my-vector-sidecar
  args: ["--db", "/var/lib/mydb"]

queue:
  backend: external
  command: /usr/local/bin/my-queue-sidecar
```

!!! note "Sidecar crash behaviour"
    Storage sidecars are not auto-respawned in v1 — a crashed sidecar
    fails calls with a clear error. Restart the gateway (or fix the
    sidecar) to recover.

## Knowledge & embeddings (`knowledge:`)

RAG defaults used by knowledge bases:

| Key | Default | Description |
|-----|---------|-------------|
| `db_path` | `<workspace>/data/knowledge.db` | Knowledge SQLite database |
| `embedding_provider` | `ollama` | Embedding provider |
| `embedding_model` | `nomic-embed-text` | Embedding model |
| `chunk_size` | `1000` | Chunk size in characters |
| `chunk_overlap` | `200` | Chunk overlap in characters |

```yaml
knowledge:
  embedding_provider: ollama
  embedding_model: nomic-embed-text
  chunk_size: 1000
  chunk_overlap: 200
```

The default embedding model (`nomic-embed-text`) produces 768-dimension
vectors, matching the `vector.dims` / `memory.vector_dims` default of
`768`. If you change embedders, keep these in sync.

The empty legacy `memory.vector_db` value no longer disables the modern vector
layer when `vector.backend` is also empty; runtime selects sqlite-vec. To use a
different implementation, set `vector.backend` explicitly.

## Memory settings (`memory:`)

| Key | Default | Description |
|-----|---------|-------------|
| `dir` | `<workspace>/memory` | Base directory for file memory |
| `sqlite_path` | `<workspace>/data/archive.db` | SQLite memory archive |
| `vector_db` | `""` | Legacy compatibility toggle; the modern effective default remains sqlite-vec |
| `vector_dims` | `768` | Embedding dimensions |
| `max_history` | `50` | Max messages kept in hot memory |

## Choosing backends

| Deployment | storage | vector | queue |
|------------|---------|--------|-------|
| Laptop / single user | `sqlite` | `sqlite-vec` | `memory` |
| Single VPS, external observers | `sqlite` | `sqlite-vec` | `external` |
| Multi-instance / heavy traffic | `postgres` | `external` | `external` |
| Custom database stack | any | `external` | `external` |

The `qdrant` and `nats` backends are **experimental and unsupported** (no
tests, no known users). For cross-process vector search or queueing, prefer an
`external` sidecar.
