# Plugin Principals & Capabilities

Status: v1 (Story E5) · Code: `internal/caps` · Manifest types: `pkg/plugin`

Plugins are **security principals, not trusted code**. Everything a plugin may
do against the host API is declared in its manifest and enforced at the
host-API boundary. The default is **deny**: a plugin with no `permissions`
block can do nothing.

## Principals

A plugin acts as the principal `plugin:<id>` (e.g. `plugin:matrix-suite`).
This is distinct from user roles (`admin`/`operator`/`viewer`, see
`internal/rbac`) — **the user RBAC model is untouched**. The capability
middleware acts only on requests whose authenticated subject starts with
`plugin:`; user requests pass straight through to the existing RBAC chain.

## Capability grammar

A capability is `resource.action` (lowercase, one dot) plus a scope list.
Which scope list applies is fixed per capability when it is registered.

```yaml
# plugin.yaml
permissions:
  - cap: vector.search        # scoped by agents
    agents: [support-bot]
  - cap: channel.send         # scoped by channels
    channels: [matrix]
  - cap: events.subscribe     # scoped by event types (docs/EVENTS.md)
    types: [run.finished]
```

Semantics:

| Declaration | Effect |
|---|---|
| cap not declared | denied (default-deny) |
| cap unknown to the host | manifest invalid → plugin refused at load |
| scope list of the wrong kind (e.g. `channel.send` with `agents:`) | manifest invalid → plugin refused at load |
| declared, scope list empty | allowed for any scope value |
| declared, `"*"` in the list | allowed for any scope value |
| declared with values | allowed only for listed values; unscoped use refused |

Duplicate declarations of the same cap are merged (union of scopes).

## Initial capability set (v1)

| Capability | Scope kind | Grants |
|---|---|---|
| `vector.search` | `agents` | vector/knowledge search against the listed agents |
| `channel.send` | `channels` | outbound sends on the listed channels |
| `events.subscribe` | `types` | subscription to the listed event types |

## Enforcement & audit

- `caps.Enforcer` holds one compiled `caps.Set` per loaded plugin
  (`SetPluginSet`/`RemovePluginSet`; the loader builds the set from the
  manifest and refuses plugins with invalid permissions).
- Service code calls `Enforcer.Check(principal, cap, scope)`; Fiber routes use
  `Enforcer.RequireCapability(cap, scopeFn)`.
- **Every decision — allow and deny — is written to the audit log**
  (`internal/audit`), with `SessionID = "plugin:<id>"` (one audit file per
  plugin per day), `Tool = "cap:<capability>"`, the scope in `Args`, `Denied`
  set on refusal, and the reason in `Error`. Denies are additionally logged
  via zap.
- Plugin principals cannot satisfy user RBAC checks and vice versa: a
  non-plugin principal sent through `Check` is denied, and plugin requests
  never reach the RBAC role policy with a role.

Scoped plugin tokens (Story E8) authenticate requests as plugin principals:
`POST /api/v1/plugins/:id/token` (user-authenticated) mints an opaque
`splg_…` bearer token bound to `plugin:<id>`. At the API layer plugin
principals pass through `pluginGateMW` (internal/gateway/plugins.go): a
route policy table maps allowed routes to capabilities — anything unlisted
is 403, listed routes go through `Enforcer.Check`. Initial table: `GET
/api/v1/health` (no cap) and `POST /api/v1/knowledge/:kb/search`
(`vector.search`, unscoped — agent-restricted grants are refused there
until per-agent scoping lands). Grow the table alongside the registry: one
entry, one cap, one allow + one deny test.

## Adding a new capability

1. Pick a name in the `resource.action` grammar and the scope kind that
   limits it (`ScopeAgents`, `ScopeChannels`, or `ScopeTypes`; new scope
   kinds require extending `pkg/plugin.Permission` and `internal/caps`).
2. Add a `Cap*` constant in `internal/caps/caps.go` and register it in
   `init()` — or call `caps.Register(name, kind)` from the subsystem that
   owns the resource.
3. Enforce it at the host boundary: `Enforcer.Check` in service code or
   `Enforcer.RequireCapability` on routes.
4. Document it in the table above and add an allow + a deny test.

Compatibility: capability names are append-only. Renaming or removing a
capability breaks existing manifests and requires a manifest-schema bump
(see `docs/EXTENSIBILITY.md` compatibility policy).

## WebSocket event stream (Story 19c)

`/ws/events` accepts scoped plugin tokens: a plugin whose manifest grants
`events.subscribe` may connect with its `splg_` token (Authorization header
or `?api_key=` for clients that cannot set headers). Tokens WITHOUT the
grant get 403 — the feed carries prompts and tool I/O, so a bare valid
token is never enough. User credentials authenticate exactly as before.
