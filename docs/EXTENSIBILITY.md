# Soulacy Extensibility Blueprint

Status: **approved design, not yet implemented** (branch: `feature/extensibility-blueprint`)
Last updated: 2026-06-06
Owners: Vasu + Claude sessions

This document is the source of truth for making Soulacy independently
extensible by third-party developers **without losing its core strengths**:
a single static Go binary, low footprint, fast local vector lookups, and the
existing security model (RBAC, credentials vault, audit logging, sandboxed
tool execution).

---

## 1. Guiding principles

1. **The stock binary stays batteries-included and self-contained.** No
   dynamic code loading (`plugin.so` is platform-fragile and version-locked;
   it would destroy single-binary portability).
2. **Extensibility comes from stable wire protocols at runtime** (sidecars,
   webhooks, MCP) **or a registry + rebuild for Go-native code** (the
   Caddy/CoreDNS/Terraform trade). Never both for the same feature.
3. **Plugins are security principals, not trusted code.** Everything a plugin
   can do is declared in its manifest and enforced at the host boundary.
4. **Contracts are versioned from day one.** SDK semver, protocol handshake
   versions, event schema versions, manifest schema versions.
5. **Least disruption: strangler pattern.** New mechanisms run alongside
   existing wiring; nothing is rewritten until the replacement is proven.

---

## 2. Current architecture (as-is, 2026-06-06)

```
                              ┌──────────────────────────────────────────┐
                              │       soulacy (single Go binary)         │
                              │                                          │
 Telegram/Slack/Discord ──┐   │  ┌────────────┐      ┌────────────────┐  │
 WhatsApp (Cloud API)  ───┼──►│  │  channels   │ msg  │    runtime     │  │
 WhatsApp Web (sidecar)───┤   │  │  Registry   ├─────►│    Engine      │  │
 HTTP webhooks ───────────┘   │  └────────────┘      │  (agent loop)  │  │
                              │  ┌────────────┐      └───┬────────┬───┘  │
 GUI (Svelte, embedded) ─────►│  │  gateway    │          │        │      │
 REST /api/v1 + /ws/events    │  │  (Fiber)    │◄─────────┘        ▼      │
                              │  └────────────┘  events   ┌────────────┐ │
                              │  ┌────────────┐           │ llm.Router │ │
                              │  │ scheduler  │──────────►│ providers: │ │
                              │  └────────────┘           │ ollama/    │ │
                              │  ┌─────────────────────┐  │ openai/    │ │
                              │  │ stores: sqlite (def) │  │ anthropic/ │ │
                              │  │ or postgres; vector: │  │ gemini/    │ │
                              │  │ sqlite-vec or qdrant;│  │ any OpenAI-│ │
                              │  │ queue: memory or NATS│  │ compatible │ │
                              │  └─────────────────────┘  └────────────┘ │
                              └──────────────────────────────────────────┘
                                     │              │             │
                                     ▼              ▼             ▼
                               MCP servers    Python tools   Node sidecar
                               (stdio/HTTP)   (subprocess/   (whatsappweb,
                                              pool, rlimit   NDJSON stdio)
                                              sandbox)
```

### Extension seams that already exist

| Seam | Mechanism | Recompile needed? |
|------|-----------|-------------------|
| Storage / queue / vector / executor backends | Go interface + config switch in `main.go` | To add new impls, yes |
| LLM providers | `llm.Provider` interface + `Router.Register`; any `api_key`+`base_url` config auto-wraps as OpenAI-compatible | New native providers, yes; OpenAI-compatible, **no** |
| Channels | `channels.Adapter` interface + `Registry.Register`; hot-replace via `StartAdapter` | Yes (hardcoded in `main.go`) |
| Agent tools | SOUL.yaml `tools:` (python_file / inline), MCP servers, plugin Python tools | **No** |
| Skills | SKILL.md dirs (agentskills.io spec), catalog injected into prompts | **No** |
| Plugins | `plugin.yaml` manifest → Python tools only today; `pkg/plugin.Registry` declares `RegisterChannel/RegisterProvider/RegisterToolLibrary` but is **never wired** | **No** (but only tools work) |
| Events | EventHub → WebSocket `/ws/events` only; queue backend wired but **unconsumed**; no outbound webhooks | n/a |

### The gaps

1. Go-native extensions (channel, provider, backend) require **forking the
   repo** and editing a ~1300-line `main.go`.
2. Extension interfaces live in `internal/` — no importable, versioned SDK.
3. No outbound event delivery (webhooks/queue) for zero-code observers.
4. Plugin manifest supports tools only; channels/providers keys parse but do
   nothing.
5. No plugin identity: anything in-process runs with full authority.
6. No GUI extension point.

---

## 3. Target architecture: extension tiers

```
┌───────────────────────────────────────────────────────────────────────────┐
│                            Soulacy Gateway                                │
│                                                                           │
│  Tier 0: Data/config (TODAY)   Tier 2: Sidecars (stdio NDJSON)            │
│  - SOUL.yaml agents            - channels in any language                 │
│  - SKILL.md skills             - tool servers (= MCP, already shipped)    │
│  - MCP servers                 - credential delegation from vault         │
│  - OpenAI-compatible providers - supervised lifecycle, rlimit sandbox     │
│                                                                           │
│  Tier 1: Observers (events)    Tier 3: Go SDK (compile-time)              │
│  - signed outbound webhooks    - sdk module: Adapter/Provider/Backend     │
│  - queue (NATS) subscribers    - factory registries + init() drivers      │
│  - zero plugin runtime         - `soulacy build --with …` flavored binary │
│                                                                           │
│  Tier 4 (DEFERRED): WASM in-process transforms — revisit on demand only   │
└───────────────────────────────────────────────────────────────────────────┘
```

Decisions locked in (after review):

- **WASM deferred indefinitely.** DX too rough (TinyGo stdlib gaps, host-API
  compatibility burden). Skills + sidecars cover the use cases.
- **Sandboxing baseline = existing rlimit `__exec-sandbox` re-exec wrapper**
  (portable, already shipped). `nsjail`/containers are optional Linux-only
  hardening, never a feature gate. macOS support is first-class.
- **One sidecar protocol: NDJSON / JSON-RPC over stdio.** No gRPC (protoc
  burden on plugin authors; MCP ecosystem familiarity wins).
- **Plugins are distinct principals** (`plugin:<id>`), not users with roles.
- **Go SDK is a separate Go module** so it versions independently of the app.

---

## 4. Phase 1 — Observer layer (outbound events)

**Objective:** publish engine/gateway events to the (already wired, currently
unconsumed) queue backend and to user-configured webhook endpoints. Enables
dashboards, alerting, log shippers, approval bots — zero compile, zero
process management.

### Event schema v1

Every payload carries an explicit schema version:

```json
{
  "schema": 1,
  "id": "evt_01J…",
  "type": "run.failed",          // message.in|message.out|tool.call|tool.result|error|run.started|run.finished|run.failed
  "agent_id": "support-bot",
  "session_id": "wb-12-…",
  "ts": "2026-06-06T21:04:05Z",
  "data": { … type-specific … }
}
```

Workboard run events (Story 6/7 data) are first-class event types.

### Delivery design

- **Webhooks route through the queue backend** (memory or NATS) as the
  buffer — never inline from `EventHub.Emit()`. Emit stays non-blocking; the
  engine is never slowed by a dead endpoint. (EventHub deliberately drops
  slow WS clients; webhooks need buffering + retry instead, hence the queue.)
- **Signing:** `X-Soulacy-Signature: t=<unix>,v1=<hex hmac-sha256>` over
  `<t>.<body>`, secret from env/vault per endpoint. Reject skew > 5 min.
- **Guarantee: best-effort with N retries** (default 5, exponential backoff
  with jitter, cap 10 min), then drop + `webhook.dead` audit entry.
  Explicitly **not** at-least-once in v1. Document this.
- **Config:**

```yaml
hooks:
  - on: [run.failed, tool.call]      # event type filter; "*" allowed
    agents: [support-bot]             # optional agent filter
    url: https://ops.example.com/soulacy
    secret_env: SOULACY_HOOK_SECRET
```

### Disruption: **near zero**

New `internal/hooks` package + ~20 lines in EventHub/main.go. No existing
behaviour changes; feature absent unless `hooks:` configured. Queue backend
finally earns its keep.

---

## 5. Phase 2 — Sidecar channels, plugin.yaml v2, plugin principals

**Objective:** generalize the proven WhatsApp Web NDJSON sidecar into a
documented External Channel Protocol; deliver credential delegation and the
plugin principal model; first GUI mounts.

> Precondition: the in-flight whatsappweb/channel-mapping work must be merged
> and stable — it is the code being generalized.

### 5.1 External Channel Protocol v1 (stdio NDJSON)

Frames (one JSON object per line):

```
sidecar → gateway:
  {"type":"hello","protocol":1,"name":"matrix","capabilities":["send","status"]}
  {"type":"status","connected":true,"detail":"…"}
  {"type":"message","id":"…","chat_id":"…","sender_id":"…","text":"…","ts":…}
  {"type":"error","detail":"…"}

gateway → sidecar:
  {"type":"hello_ack","protocol":1}        // version negotiation: gateway may
                                            // answer with lower mutual version
  {"type":"send","to":"<chat_id>","text":"…"}
  {"type":"shutdown"}
```

- **Version negotiation lives in the handshake** (not deferred to Phase 3).
  Unknown frame types are ignored (forward compatibility).
- Generic `ExternalChannelAdapter` in Go implements `channels.Adapter` once;
  whatsappweb becomes its first consumer (or stays as-is until convenient —
  no forced migration).

### 5.2 Supervision lifecycle

Sidecars are supervised: spawn → handshake (5 s deadline) → healthy; on crash,
restart with exponential backoff (1 s → 60 s cap, reset after 10 min healthy);
`shutdown` frame + SIGTERM grace 5 s + SIGKILL on Stop. Status surfaces in the
existing Channels GUI via `AdapterStatus`.
Sandbox baseline: spawn through the existing rlimit re-exec wrapper.

### 5.3 Credential delegation

Manifest declares needs; vault injects **only those** at spawn:

```yaml
credentials:
  - key: MATRIX_TOKEN        # env var name in the sidecar
    from: matrix-suite/token # vault path (scoped to this plugin's namespace)
```

- v1 transport: environment variables (simple, language-agnostic). Known
  limitations documented: visible in `/proc` on shared hosts; stale after
  rotation. **Rotation story: gateway restarts the sidecar on vault
  rotation** (vault already versions secrets). Higher-sensitivity transport
  (handshake-frame delivery) is a v2 option, not v1.

### 5.4 Plugin principal + capability model

New principal type `plugin:<id>` — **needed in this phase** (the GUI mount
below must not see user API keys).

Capability grammar (manifest-declared, host-enforced):

```yaml
permissions:
  - cap: vector.search          # resource.action
    agents: [support-bot]       # scope
  - cap: channel.send
    channels: [matrix]
  - cap: events.subscribe
    types: [run.finished]
```

Enforced at the host-API boundary (gateway service layer), logged to the
audit log. Default: **no capabilities**.

### 5.5 plugin.yaml v2

```yaml
id: matrix-suite
version: 1.0.0
manifest_schema: 2
channels:
  - id: matrix
    sidecar: { command: "node", args: ["sidecar/matrix.mjs"] }
providers:
  - id: local-vllm
    openai_compatible: { base_url: "http://localhost:8000/v1", api_key_env: VLLM_KEY }
tools:        # existing python: mechanism, unchanged
  - rooms.py
skills:
  - skills/moderation/
gui:
  nav: { label: "Matrix", icon: "💬" }
  static: ui/
credentials: [ … see 5.3 … ]
permissions: [ … see 5.4 … ]
```

`pkg/plugin.Registry` (already declared, never wired) becomes real:
channels → sidecar adapters; providers → OpenAI-compatible auto-wrap (no new
Go code path needed); tools → existing loader.

### 5.6 GUI mounts

Static assets served at `/plugins/<id>/ui/`, rendered in a sandboxed
`<iframe>`; nav entry from manifest. The iframe receives a **scoped plugin
token** (JWT-ish, principal `plugin:<id>`, its capabilities only) — never the
user's API key.

### Disruption: **low**

New packages (`internal/sidecar`, principal checks), additive loader changes,
one new route group for GUI mounts. `main.go` channel wiring untouched —
sidecar channels arrive via the plugin loader, not via editing core wiring.
Existing channels keep working unmodified.

---

## 6. Phase 3 — Go SDK, registries, flavored builds

**Objective:** let independent developers ship Go-native extensions without
forking, and shrink `main.go` from wiring monolith to composition root.

### 6.1 Separate SDK module

`github.com/soulacy/soulacy/sdk` (own `go.mod`, own semver):

```
sdk/
  channel/   Adapter, AdapterStatus, RegisterFactory(id, Factory)
  provider/  Provider, CompletionRequest/Response, RegisterFactory
  backend/   queue.Backend, vector.Backend, storage interfaces, RegisterFactory
  tool/      BuiltinTool shape, RegisterLibrary
  message/   re-export of pkg/message (canonical types)
  *test/     conformance kits: channeltest.RunAdapterSuite(t, a), providertest…
```

Separate module ⇒ gateway releases ≠ SDK releases; compatibility promises are
independent (HashiCorp pattern).

### 6.2 Least-disruptive interface promotion — **type aliases**

The migration trick that makes this near-zero-risk:

```go
// internal/channels/channel.go (after promotion)
type Adapter = sdkchannel.Adapter          // alias, not a copy
type AdapterStatus = sdkchannel.AdapterStatus
```

Aliases are identical types to the compiler ⇒ **every existing file,
implementation, and test keeps compiling unchanged**. Move method sets, not
call sites. Same for `llm.Provider` and backend interfaces.

### 6.3 Registries + driver self-registration

`database/sql`-style:

```go
// third-party module
func init() { sdkchannel.RegisterFactory("matrix", New) }
```

Built-ins register the same way via blank imports collected in one generated
file (`cmd/soulacy/imports_gen.go`). `main.go` resolves `config.channels`
entries against the registry **with fallback to the existing hardcoded
switch** during transition (strangler: delete the switch only when every
built-in is registry-routed and tests pass).

### 6.4 `main.go` decomposition

Extract `internal/app`: `app.New(cfg, opts...) (*App, error)` building engine,
stores, gateway from registries. `cmd/soulacy/main.go` shrinks to flag/config
parsing + `app.New().Run()`. Do this **incrementally** — one subsystem per PR
(stores → providers → channels → adapters misc), full test suite green after
each step. A custom distribution becomes:

```go
package main

import (
    "github.com/soulacy/soulacy/internal/app"
    _ "github.com/alice/soulacy-matrix"   // registers "matrix"
)

func main() { app.Main() }
```

### 6.5 Build helper

`soulacy build --with github.com/alice/soulacy-matrix@v1.2.0` → writes the
blank-import file, runs `go build` → **still one static binary**. (xcaddy
pattern; can start as a 100-line script in `scripts/`.)

### Disruption: **moderate but bounded**

The only phase touching core structure. Mitigations: type aliases (6.2) keep
all imports valid; registry-with-fallback (6.3) keeps boot behaviour
identical; decomposition (6.4) is incremental with green-suite checkpoints;
no on-disk/config format changes; users notice nothing.

---

## 7. Phase 4 — WASM (deferred indefinitely)

Revisit only if a concrete need appears for sandboxed, hot-loadable,
in-process logic that skills/sidecars demonstrably can't serve (e.g.
per-token streaming transforms). If revived: `wazero` (pure Go, keeps single
binary), pure `bytes→bytes` transforms only, hard context deadline, no host
API beyond the input payload.

---

## 8. Compatibility policy (draft)

- **SDK module:** semver. Breaking interface changes ⇒ major bump. Additive
  methods via extension interfaces (`interface{ Foo() }` type-asserts), never
  by widening existing interfaces.
- **Sidecar protocol:** integer version in handshake; gateway supports
  current + previous major for ≥ 2 releases; unknown frames ignored.
- **Event schema:** `schema` field; additive fields allowed without bump.
- **Manifest:** `manifest_schema` field; loader warns + best-effort on older.
- **REST:** `/api/v1` discipline as today.

---

## 9. Disruption summary & sequencing vs. the backlog

| Phase | New code | Core code touched | Breaking risk | Prereq |
|-------|----------|-------------------|---------------|--------|
| 1 Observers | `internal/hooks` | EventHub +emit hook, main.go ~10 lines | none | Story 7 lands run-level data first |
| 2 Sidecars | `internal/sidecar`, principal checks, loader v2 | plugin loader (additive), gateway routes (additive) | none | channels/whatsappweb work merged & stable |
| 3 SDK | `sdk/` module, registries, `internal/app` | interface homes (aliased), main.go (incremental) | low (mitigated) | none, but do after sprint stories to avoid churn |
| 4 WASM | — | — | — | explicit demand |

**Recommended interleaving with the 15-story sprint:**
Story 7 (run observability) → **Phase 1** (its natural exhaust) → Stories 8–9
→ **Phase 2** (channels work settled by then) → remaining stories →
**Phase 3** as the post-sprint structural investment.

All extensibility implementation happens on feature branches off `main`
(this doc lives on `feature/extensibility-blueprint`), TDD as per standing
rules, commits on green.

---

## 10. Open questions

1. Plugin distribution/discovery (marketplace? git URLs? checksummed
   archives?) — Phase 2/3 decision, not needed for design sign-off.
2. Should sidecar providers exist at all, or is OpenAI-compatible HTTP enough?
   (Current lean: HTTP shim is enough; revisit on demand for streaming-native
   protocols.)
3. Capability grammar granularity — start with the ~6 caps in §5.4 and grow,
   or design the full matrix upfront? (Current lean: start small.)
4. Whether `pkg/message` moves into the SDK module or is re-exported.
