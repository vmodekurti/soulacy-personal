# API Reference

Everything the GUI can do, the REST API can do — the GUI is just a client.
The API lives under `/api/v1` on the gateway port.

## Base URL

```
http://localhost:1947/api/v1
```

The port comes from `server.port` (default `1947`).

## Authentication

Send a bearer token on every request:

```bash
curl -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:1947/api/v1/agents
```

| Mode | How it works |
|------|--------------|
| `apikey` (default) | The static `server.api_key` from config.yaml is the bearer token. Empty key = **open gateway** (dev only; the server logs a warning) |
| `jwt` | Exchange the static key for a short-lived JWT pair via `POST /api/v1/auth/token`; rotate with `POST /api/v1/auth/refresh`. The static key is still accepted |
| OIDC | With `auth.oidc_issuer` set, JWTs from that issuer are also accepted |

See [Auth configuration](../configuration/auth.md) and the
[Auth API](auth.md). `GET /ping` (unauthenticated) reports the auth
posture and mode. WebSocket clients that cannot set headers may pass the
credential as `?api_key=` on `/ws/events`.

## Conventions

- Requests and responses are JSON (`Content-Type: application/json`).
- Errors use a stable envelope: `{"error": "<message>"}` with an
  appropriate HTTP status (`401`, `403`, `404`, `429`, `503`, …).
- Optional subsystems (workboard, costs, credential vault, DLQ, history)
  return `503` with an explanatory error until wired/enabled.
- IP-level rate limit: 600 requests/minute, JSON API surface only.

## Route catalog

### Health & identity

| Route | Description |
|-------|-------------|
| `GET /ping` | Unauthenticated liveness + auth posture |
| `GET /api/v1/health` | Authenticated health check |
| `POST /api/v1/auth/token` · `POST /api/v1/auth/refresh` · `GET /api/v1/auth/me` | JWT issuance, rotation, identity ([Auth](auth.md)) |

### Agents

| Route | Description |
|-------|-------------|
| `GET /agents` · `GET /agents/:id` | List / inspect agents |
| `POST /agents` · `PUT /agents/:id` · `DELETE /agents/:id` | Create / update / delete |
| `POST /agents/validate` | Validate a SOUL.yaml without deploying |
| `POST /agents/:id/enable` · `…/disable` | Toggle an agent |
| `POST /agents/:id/trigger` | Manually fire a scheduled agent |
| `POST /agents/:id/clone` | Clone an agent definition |
| `GET /agents/:id/actions` | Per-agent action log |

### Chat, sessions & history

| Route | Description |
|-------|-------------|
| `POST /chat` | Send a message, get the reply |
| `POST /chat/feedback` | Rate a completed response (`rating`: `1` or `-1`) |
| `POST /chat/stream` · `GET /chat/stream` | Streamed reply (SSE) |
| `POST /chat/confirm` | Answer a pending tool-confirmation prompt |
| `GET /learning/feedback` | Review recent response-level feedback signals |
| `GET /history/:session_id` · `GET /history/agent/:agent_id` | Conversation history |
| `POST /history/:session_id/fork` | Fork a conversation at a checkpoint into a new branch |
| `GET /runs/:session_id/metrics` | Run-level observability for one session |

### Schedule

| Route | Description |
|-------|-------------|
| `GET /schedule` · `GET /schedule/status` | Scheduled entries and scheduler status |

### Workboard

Task CRUD, runs, artifacts (incl. download), comments — see
[Workboard API](workboard.md). Base paths: `/workboard/tasks`,
`/workboard/artifacts/:id/download`, `/workboard/comments/:id`.

### Knowledge (RAG)

| Route | Description |
|-------|-------------|
| `GET /knowledge` · `POST /knowledge` · `DELETE /knowledge/:kb` | Manage knowledge bases |
| `GET /knowledge/:kb/documents` · `POST …/documents` · `DELETE …/documents/:doc` | Manage documents |
| `POST /knowledge/:kb/search` | Semantic search within a KB |

### Memory & brain memory

| Route | Description |
|-------|-------------|
| `GET /memory/:agent_id` · `DELETE /memory/:agent_id/:session_id` | Session memory |
| `GET /brain-memory` | Stats across the three memory layers |
| `GET/POST/DELETE /brain-memory/:agentID/episodic` | Episodic memory |
| `GET/PUT/DELETE /brain-memory/:agentID/procedural` | Procedural memory (rulebook) |
| `POST /brain-memory/:agentID/context-preview` | Preview assembled context |
| `GET /brain-memory/:agentID/rulebook[/:version]` | Versioned rulebook history |
| `POST …/rulebook/rollback` · `POST …/rulebook/lock` | Roll back / lock the rulebook |

### Skills & registries

`GET /skills`, `GET /skills/:name`, `POST /skills/rescan`,
`POST /skills/provision-agenticskills`; `GET/POST /registries`,
`POST /registries/probe` — see [Registries & Skills](registries.md).

### Plugins

| Route | Description |
|-------|-------------|
| `GET /plugins/ui` · `POST /plugins/:id/token` | Plugin GUI mounts + scoped iframe tokens |
| `GET /plugins/installed` | Installer-owned plugins |
| `POST /plugins/install` → `POST /plugins/install/:staged/approve` / `DELETE /plugins/install/:staged` | Stage → approve/discard flow |
| `POST /plugins/:id/enable` · `…/disable` · `…/reapprove` · `DELETE /plugins/:id` | Lifecycle ([Plugins](../extend/plugins.md)) |

### Providers, MCP & tools

| Route | Description |
|-------|-------------|
| `GET /providers` · `GET /providers/:id/models` | LLM providers and models |
| `POST /providers/:id/model` · `POST /providers/:id` | Set model / credentials |
| `GET/POST /mcp` · `PATCH/DELETE /mcp/:id` · `POST /mcp/test` | Manage MCP servers |
| `GET /mcp/registry/search` · `POST /mcp/provision-registry` · `POST /mcp/provision-glama` | Discover & provision MCP servers |
| `GET /tool-catalog` | Unified catalog: python tools + MCP tools + Go built-ins |

### Builder & templates

| Route | Description |
|-------|-------------|
| `POST /builder/chat` · `…/generate` · `…/deploy` · `DELETE /builder/session/:id` | Conversational agent builder |
| `POST /builder/analyze` · `…/resolve` | Capability gap detection |
| `GET /templates` · `POST /templates/:name/instantiate` | Starter agent templates |

### Channels

| Route | Description |
|-------|-------------|
| `GET /channels` · `PATCH /channels/:id` | List / update channel adapters |
| `POST /channels/:id/enable` · `…/disable` | Toggle channels |
| `POST /channels/whatsapp_web/pair` | Start WhatsApp Web QR pairing |
| `GET/POST /channels/whatsapp/webhook` | Meta webhook (unauthenticated; HMAC-verified) |

### Config & registries

| Route | Description |
|-------|-------------|
| `GET /config` · `PATCH /config` | Read / write config.yaml (secrets redacted) |

### Events, voice & costs

| Route | Description |
|-------|-------------|
| `GET /ws/events` (WebSocket) | Live event stream ([Events](../configuration/events.md)) |
| `GET /voice/status` · `POST /voice/ephemeral` | Voice availability + ephemeral client keys ([Voice](../configuration/voice.md)) |
| `GET /costs` · `GET /costs/:agent_id` | Token-cost summaries ([Costs](costs.md)) |
| `GET /costs/status` | Pricing, budget, reservation, attribution, forecast, and reconciliation readiness |
| `POST /costs/estimate` | Prompt-free estimate from provider/model and token counts |
| `GET /costs/usage` · `GET /costs/chargeback` | Bounded call records and grouped allocation |
| `GET /costs/reconciliations` · `POST /costs/reconcile` | Inspect or record provider-billing reconciliation |
| `GET /rate-limit/status` | Current quota state |
| `GET /metrics` | Prometheus metrics (same auth as the API) |

### Credentials

`POST/GET /credentials/:agentID`, `GET/DELETE /credentials/:agentID/:key`,
`POST …/:key/rotate`, `GET …/:key/versions` —
see [Credentials](credentials.md).

### RBAC & admin

| Route | Description |
|-------|-------------|
| `GET /rbac/policy` · `GET /rbac/grants[/:role]` | Inspect RBAC policy and grants |
| `PUT/DELETE /rbac/grants/:role/:agent_id` | Set / remove per-agent grants |
| `POST/GET /admin/api-keys` · `DELETE /admin/api-keys/:id` · `POST …/validate` | Managed API keys ([Admin](admin.md)) |
| `GET /admin/dlq` · `GET/DELETE /admin/dlq/:id` | Dead-letter queue |
| `GET /admin/dashboard` | Observability dashboard data |
| `POST /admin/restart` | Restart the gateway |
| `GET /logs` | Tail the gateway log file |
