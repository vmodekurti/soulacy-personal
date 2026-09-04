# Registries & Skills API

These endpoints manage **skill sources** (package registries), review
candidate URLs before trusting them, and hot-load skills — the API behind
the GUI's "Add source" / "Install skill" flows and the
`sy registry` / `sy skill install` CLI commands.

All routes live under `/api/v1` and require
[authentication](index.md#authentication). Registry routes are gated by
config-level RBAC (they read and write `config.yaml`).

## Skill sources (registries)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/registries` | List configured sources (auth headers redacted) |
| `POST` | `/registries/probe` | Review a URL: what kind of source is it? |
| `POST` | `/registries` | Add a reviewed source to `config.yaml` |

### List sources

```bash
curl -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:18789/api/v1/registries
```

```json
{
  "registries": [
    { "id": "main", "type": "http", "base_url": "https://registry.example.com",
      "priority": 10, "has_auth": true, "signing_key": "3b6a27bc…" },
    { "id": "github", "type": "git", "priority": 100, "has_auth": false }
  ],
  "count": 2
}
```

`auth_headers` values never leave the server — only the `has_auth` flag
is exposed. `signing_key` is the registry's *public* ed25519 key, so it
is returned as-is.

### Probe a URL

Probe before you trust: the server fetches the URL and classifies it
(skills.sh-style directory, E19 package registry, git host, or plain
page), returning sample packages and a suggested config entry.

```bash
curl -X POST http://localhost:18789/api/v1/registries/probe \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://www.skills.sh/"}'
```

The response is a probe report: `url`, `kind`, `detail`, optional
`samples`, and a `suggested` registry entry you can pass straight to
`POST /registries`. Only `http`/`https` targets are accepted; a bad or
unreachable URL returns `400`.

### Add a source

```bash
curl -X POST http://localhost:18789/api/v1/registries \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "main",
    "type": "http",
    "base_url": "https://registry.example.com",
    "priority": 10
  }'
```

Body fields:

| Field | Required | Description |
|-------|----------|-------------|
| `id` | no (defaults to `type`) | Operator-chosen name shown in consent dialogs |
| `type` | no (default `http`) | `http` or `git` (plus any types a flavored binary registers) |
| `base_url` | for non-`git` types | Registry root URL |
| `priority` | no | Resolution order — **lower runs first** |
| `signing_key` | no | Hex ed25519 public key; when set, unsigned/tampered packages from this source are refused |

Responses: `200 {"ok": true, "message": "…"}` on success, `400` for an
unknown type or missing `base_url`, `409` when the `id` already exists,
`503` when the gateway doesn't know its config file path.
`sy skill install` picks up new sources immediately; restart the gateway
for GUI installs.

## Skills

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/skills` | List all loaded Agent Skills |
| `GET` | `/skills/:name` | Full skill instructions |
| `POST` | `/skills/rescan` | Rescan skill directories and hot-load changes |
| `POST` | `/skills/provision-agenticskills` | Provision a skill from the agenticskills catalog |

`POST /skills/rescan` is how `sy skill install` hot-loads a freshly
installed skill without a gateway restart:

```bash
curl -X POST -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:18789/api/v1/skills/rescan
```

## Plugin installs (summary)

Plugins follow a stage → review → approve lifecycle with a safety
introspection step; see [Plugins](../extend/plugins.md) for the full
flow and payloads.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/plugins/installed` | List installer-owned plugins |
| `POST` | `/plugins/install` | Stage a plugin and return the install preview (permissions, fingerprint, security verdict) |
| `POST` | `/plugins/install/:staged/approve` | Approve a staged plugin |
| `DELETE` | `/plugins/install/:staged` | Discard a staged plugin |
| `POST` | `/plugins/:id/enable` / `…/disable` | Toggle an installed plugin |
| `POST` | `/plugins/:id/reapprove` | Re-approve after a permissions change |
| `DELETE` | `/plugins/:id` | Remove an installed plugin |

## How resolution works

When you install by slug (`sy skill install self-improving-agent` or the
GUI flow), configured registries are queried in ascending `priority`
order with fallback — the first registry that resolves the slug wins.
With no registries configured, a bare `git` provider still resolves
`github.com/user/my-skill` sources. Every remote install runs the safety
introspection pipeline and requires consent before files land in the
workspace `skills/` directory.

## See also

- [Skill Sources](../extend/skill-sources.md) — concepts and GUI flow
- [Package Registries](../extend/registries.md) — running your own registry
- [CLI Reference](../cli/reference.md) — `sy registry`, `sy skill install`
