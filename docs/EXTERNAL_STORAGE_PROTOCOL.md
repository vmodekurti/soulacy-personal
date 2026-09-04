# External Storage Protocol (ESP) v1 — Story E24

Soulacy's storage layer (vector / queue backends) can be served by a
**sidecar process** speaking JSON-RPC 2.0 over stdio, the storage twin of
the External Channel Protocol (ECP, `docs/EXTERNAL_CHANNEL_PROTOCOL.md`).
Third-party database drivers configure at runtime — no recompile, any
language.

## Wire format

One JSON-RPC 2.0 message per line (NDJSON) on the sidecar's
stdin/stdout. Three shapes:

- **request** — `{"jsonrpc":"2.0","id":N,"method":"...","params":{...}}`
- **response** — `{"jsonrpc":"2.0","id":N,"result":{...}}` or
  `{"jsonrpc":"2.0","id":N,"error":{"code":C,"message":"..."}}`
- **notification** — request without `id` (no reply expected)

Go types: `sdk/extstorage` (`Message`, `ParseMessage`, `WriteMessage`,
param/result structs). Rules:

- Unknown **methods** in requests → error `-32601`, never a crash.
- Unknown **notifications** and malformed lines → skipped silently.
- Structs grow by appending optional fields only (forward compat).
- stderr is free for sidecar logging; the host may surface it.

## Lifecycle

1. Host spawns the sidecar and sends a `negotiate` request:
   `{protocol, name, shared_dir}` — `shared_dir` is the ABSOLUTE path of
   the per-run scratch directory (see Shared mounts).
2. Sidecar responds `{protocol: min(host, sidecar), name, capabilities,
   shared_dir}` — it MUST echo `shared_dir` (contract proof) and MUST
   advertise at least one capability: `"vector"`, `"queue"`.
3. Backend calls follow (below).
4. `shutdown` request → respond (optional) and exit within 5 seconds.
   A sidecar that fails negotiation is killed without grace.

## Methods

### vector (mirrors `sdk/vector.Backend`)

| method | params | result |
|---|---|---|
| `vector.write` | `{id, agent_id, session_id?, scope?, content?, timestamp?, content_file?}` | `{ok}` |
| `vector.search` | `{agent_id?, query, top_k}` | `{results: [{id, agent_id, content, distance, …}]}` |

`distance`: lower = more similar. Empty `agent_id` searches all agents.

### queue (mirrors `sdk/queue.Backend`)

| method | params | result |
|---|---|---|
| `queue.publish` | `{subject, data(base64)}` | `{ok}` |
| `queue.subscribe` | `{subject, group?}` | `{subscription_id}` |
| `queue.unsubscribe` | `{subscription_id}` | `{ok}` |
| `queue.ack` | `{delivery_id}` | `{ok}` |

Deliveries flow sidecar→host as **notifications**:
`queue.message` `{subscription_id, subject, data(base64), delivery_id?}`.
Empty `delivery_id` = at-most-once (host ack is skipped). Subject
matching follows the NATS convention (`*` one token, `>` trailing).

## Shared mounts (per-run scratch directory)

Large documents/media move as **files**, not stdio JSON payloads:

- The host creates `<workspace data>/scratch/<sidecar-id>-<random>`
  (0700) before spawn — the same staging-dir discipline as E13; never a
  shared `/tmp` path — and passes it ABSOLUTE in `negotiate.shared_dir`.
- Method params reference files RELATIVE to it (e.g. `vector.write`
  `content_file`). Sidecars MUST refuse paths escaping the directory.
- The host removes the directory on Close and sweeps stale ones at boot.
- The ECP (channel) side carries the same field in `hello_ack
  {shared_dir}` — mirroring E1 resource semantics: the reference travels
  on the wire, the bytes travel on disk.

## Host configuration

```yaml
vector:
  backend: external
  command: /usr/local/bin/my-vector-sidecar
  args: ["--db", "/var/lib/mydb"]

queue:
  backend: external
  command: /usr/local/bin/my-queue-sidecar
```

Resolved through the E10 factory registries (`registry.NewVector("external", …)`
/ `NewQueue`); host adapters live in `internal/extstorage`
(`VectorBackend`, `QueueBackend` atop a shared JSON-RPC `Client`).

**Lifecycle note:** unlike channel sidecars (E4 supervisor), storage
sidecars are not auto-respawned in v1 — a crashed sidecar fails calls
with a clear error and surfaces via `Client.Done()`; restart the gateway
(or fix the sidecar) to recover. Auto-respawn with subscription replay is
a planned follow-up.

## Conformance

`sdk/extstorage/storagetest.RunConformance(ctx, sharedDir, command, args...)`
checks: negotiate ≤5s with shared-dir echo + capabilities; `-32601` on
unknown methods; vector round-trip when advertised; malformed-line
tolerance; shutdown exit ≤5s. Run it out-of-tree against your sidecar;
CI runs it against `scripts/reference-storage-sidecar.py` (python3,
dependency-free) so the contract can't drift.

## Compatibility policy

`ProtocolVersion` bumps only for breaking changes; negotiation picks the
min. Method param/result structs are append-only. New method families
(e.g. `storage.*` for action-log/memory archives) arrive as new
capabilities — old sidecars simply don't advertise them.
