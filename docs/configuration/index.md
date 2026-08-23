# Configuration Overview

Soulacy is configured by a single `config.yaml` in the
[workspace](workspace.md) — `~/.soulacy/soulspace/config.yaml` for new
installations (legacy installs keep `~/.soulacy/config.yaml`). The gateway
also honours `SOULACY_CONFIG_PATH`:

```bash
SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml soulacy
```

Start from the annotated example at the repo root:
[`config.yaml.example`](https://github.com/vmodekurti/soulacy/blob/main/config.yaml.example).

## Top-level keys

| Key | What it controls | Details |
|-----|------------------|---------|
| `deployment` | Operating mode (`personal`, `team`, `scale`) and shared artifact settings | [Deployment Modes](deployment-modes.md) |
| `server` | Host, port, API key, TLS, CORS allow-list, GUI toggle | [Server](server.md) |
| `runtime` | Max sessions/turns, Python interpreter, tool timeout, sandbox caps, system tools, SSRF protection, audit log | [Security Posture](security.md) |
| `llm` | Default provider + provider registry (Anthropic, OpenAI-compatible, Ollama, Groq, Google, …) | [LLM Providers](llm.md) |
| `channels` | Channel adapters keyed by ID: telegram, slack, discord, whatsapp, whatsapp_web, http | [Channels](../channels/index.md) |
| `mcp` | MCP servers (stdio or http); tools auto-injected as `mcp__<server>__<tool>` | [Agent Tools](../agents/tools.md) |
| `memory` | Hot-memory size, file-memory dir, memory archive, legacy vector toggle | [Storage & Backends](storage.md) |
| `storage` | Durable event-log/archive backend: sqlite (default), postgres, external | [Storage & Backends](storage.md) |
| `vector` | Vector search backend: sqlite-vec (default), qdrant, external sidecar | [Storage & Backends](storage.md) |
| `queue` | Message queue: memory (default), NATS JetStream (`nats_*` keys), external sidecar | [Storage & Backends](storage.md) |
| `executor` | Python tool executor: local backends in Personal; remote `worker` in Team/Scale | [Security Posture](security.md) |
| `knowledge` | RAG defaults: knowledge DB path, embedding provider/model, chunking | [Storage & Backends](storage.md) |
| `auth` | `apikey` (default) or `jwt` mode, JWT secret/TTLs, OIDC issuer | [Auth](auth.md) |
| `credentials` | Credential vault KMS provider: local (default), hashicorp, awskms | [Credentials API](../api/credentials.md) |
| `billing` | Provider events and webhook verification; authorization consumes provider-independent entitlements | [Production runtime](production-runtime.md) |
| `updates` | Release manifest for `sy update check/install` and launch readiness | [CLI Reference](../cli/reference.md) |
| `rate_limit` | Per-user/per-agent RPM and daily token quotas; memory or redis backend | [Rate Limiting](rate-limiting.md) |
| `telemetry` | OpenTelemetry tracing: exporter, OTLP endpoint, service name | [Telemetry](telemetry.md) |
| `hooks` | Signed outbound webhooks fed by the event stream (`on`/`agents`/`url`/`secret_env`) | [Events & Webhooks](events.md) |
| `voice` | Realtime voice panel in Chat: provider, model, base_url | [Voice](voice.md) |
| `plugins_config` | Arbitrary plugin-specific settings, keyed by plugin ID; shape owned by each plugin | [Plugins](../extend/plugins.md) |
| `registries` | Package registries for skill/plugin installs: `id`, `type`, `base_url`, `priority`, `auth_headers`, `signing_key` | [Skill Sources](../extend/skill-sources.md) |
| `agent_dirs` | Directories scanned for SOUL.yaml agent definitions | [SOUL.yaml Reference](../agents/soul-yaml.md) |
| `skill_dirs` | Extra skill directories (in addition to the workspace `skills/`) | [Installing Skills](../extend/installing-skills.md) |
| `plugin_dirs` | Plugin directories to scan | [Plugins](../extend/plugins.md) |
| `log` | Level (`debug`/`info`/`warn`/`error`), format (`json`/`console`), optional file | — |

## Minimal working config

Everything has sane defaults — a fresh install runs with no config file
at all (loopback only, Ollama provider). A typical small config:

```yaml
server:
  host: 127.0.0.1
  port: 1947
  api_key: "sy_..."          # openssl rand -hex 32 | sed 's/^/sy_/'

llm:
  default_provider: anthropic
  providers:
    anthropic:
      api_key: "sk-ant-..."
      model: claude-sonnet-4-6
      prompt_caching: true

channels:
  http:
    enabled: true

updates:
  manifest_url: https://github.com/vmodekurti/soulacy/releases/latest/download/release-manifest.json

log:
  level: info
  format: console
```

!!! tip "Use absolute paths"
    Relative paths in `agent_dirs` and similar keys resolve from the
    working directory at startup — unpredictable under LaunchAgent or
    systemd. Always use absolute paths.

## Environment variable overrides

Any config key can be overridden with an environment variable: prefix
`SOULACY_`, dots become underscores.

```bash
export SOULACY_SERVER_API_KEY="sy_..."     # server.api_key
export SOULACY_SERVER_PORT=1947            # server.port
export SOULACY_LOG_LEVEL=debug             # log.level
export SOULACY_UPDATE_MANIFEST="https://example.com/release-manifest.json"
```

Two related env vars are not config overrides:

- `SOULACY_CONFIG_PATH` — explicit config file path.
- `SOULACY_WORKSPACE` — explicit workspace root (see
  [Workspace Layout](workspace.md)).

## Config file discovery

Without `SOULACY_CONFIG_PATH`, the gateway searches in order:

1. the current working directory (project-level config wins for dev),
2. the workspace root (`~/.soulacy/soulspace` or legacy `~/.soulacy`),
3. the legacy flat `~/.soulacy`.

## Editing via API and GUI

The Settings GUI and `GET`/`PATCH /api/v1/config` read and write the same
file. Secret-looking values (keys containing `token`, `secret`,
`password`, `api_key`, `credential`) are redacted before they reach the
browser. Some changes (channels, registries for the GUI install flow)
require a gateway restart; the API response says so when they do.

!!! warning "Keep secrets out of version control"
    `config.yaml` contains API keys and tokens. Never commit it; use
    environment overrides or the credential vault for production secrets.
