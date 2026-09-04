# Plugin Security Model

Plugins are security principals, not trusted code: everything a plugin may do is declared in its manifest, granted by you at install, enforced at the host-API boundary, and audited — the default is deny.

## The model at a glance

| Layer | Mechanism |
|---|---|
| Identity | every plugin acts as the principal `plugin:<id>` |
| API access | capability grammar, default-deny, enforced per request |
| Tokens | scoped `splg_` bearer tokens bound to one plugin |
| GUI panels | sandboxed iframes, no same-origin access |
| Secrets | vault delegation in the plugin's own namespace only |
| Accountability | every allow **and** deny written to the audit log |

A plugin with no `permissions:` block can do nothing against the host API.
Plugin principals are entirely separate from user roles
(`admin`/`operator`/`viewer`): a plugin can never satisfy a user RBAC
check, and user requests never pass through the capability table.

## Capability grammar

A capability is `resource.action` (lowercase, one dot) plus a scope list.
Which scope kind applies is fixed per capability:

```yaml
# plugin.yaml
permissions:
  - cap: vector.search        # scoped by agents
    agents: [support-bot]
  - cap: channel.send         # scoped by channels
    channels: [matrix]
  - cap: events.subscribe     # scoped by event types
    types: [run.finished]
```

Semantics:

| Declaration | Effect |
|---|---|
| cap not declared | denied (default-deny) |
| cap unknown to the host | manifest invalid → plugin refused at load |
| wrong scope kind (e.g. `channel.send` with `agents:`) | manifest invalid → plugin refused at load |
| declared, scope list empty | allowed for any scope value |
| declared with `"*"` | allowed for any scope value |
| declared with values | allowed only for listed values |

Initial capability set: `vector.search` (scoped by agents),
`channel.send` (channels), `events.subscribe` (event types). Capability
names are append-only — they are never renamed or removed under a given
manifest schema.

!!! warning "Review unscoped grants carefully"
    An empty scope list or `"*"` means *any* agent, channel, or event type.
    The install approval dialog flags unscoped grants loudly — prefer
    plugins that scope every capability to exactly what they need.

## Scoped plugin tokens (`splg_`)

Plugins never hold your API key. `POST /api/v1/plugins/:id/token`
(user-authenticated) mints an opaque `splg_…` bearer token bound to
`plugin:<id>`. At the API a plugin token passes through a route policy
table: anything unlisted is 403, and listed routes still go through the
capability check. The WebSocket event stream (`/ws/events`) accepts
`splg_` tokens only when the manifest grants `events.subscribe` — a bare
valid token is never enough, because the feed carries prompts and tool I/O.

## Sandboxed GUI panels

A plugin's `gui:` mount is served at `/plugins/<id>/ui/` inside an iframe
sandboxed with `allow-scripts allow-forms` — **no same-origin**. The shell
fetches a scoped plugin token and passes it in the iframe URL fragment; the
panel uses it as its bearer token. The panel therefore cannot read your
session, your cookies, or any API the plugin was not granted.

## Credential delegation: vault namespaces

Plugin sidecars never see the gateway's environment or the vault. They
declare exactly the secrets they need:

```yaml
credentials:
  - key: MATRIX_TOKEN          # env var name inside the sidecar
    from: matrix-suite/token   # vault path: <namespace>/<key>
```

- The `from:` namespace **must equal the plugin's own ID** — referencing
  another plugin's or an agent's secrets is structurally impossible, not
  just denied.
- Plugin secrets live in the encrypted vault (AES-256-GCM) under the
  namespace `plugin:<id>`, disjoint from agent credentials by construction.
  Operators set them through the normal credentials API with
  `agent_id = "plugin:<id>"`.
- At spawn the sidecar receives a minimal allowlisted base environment
  (`PATH`, `HOME`, `TMPDIR`, `LANG`, `TZ`, …) **plus exactly the declared
  secrets** — the gateway's own API keys are never inherited.
- A declared-but-missing secret fails the spawn (retried through the
  supervisor's backoff) rather than starting a sidecar that cannot
  authenticate.

### Rotation restarts the sidecar

The vault versions secrets. A watcher polls a SHA-256 fingerprint of each
plugin's declared secrets (values are hashed, never retained or logged) and
restarts the sidecar on any change — rotation, replacement, addition, or
removal — so every respawn picks up current values.

!!! warning "Environment-variable transport has known limits"
    Secrets are injected as environment variables: visible in
    `/proc/<pid>/environ` to same-UID processes on shared hosts, fixed at
    spawn (hence the rotation restart), and inherited by the sidecar's
    children unless it is careful. Run Soulacy under its own user on shared
    machines, and treat sidecar stderr as logs — sidecars must never print
    their own credentials.

## Audit trail

Every capability decision — allow and deny — is written to the audit log
with `SessionID = "plugin:<id>"` (one audit file per plugin per day),
`Tool = "cap:<capability>"`, the scope in the arguments, and the denial
reason on refusal. Denies are additionally logged via the structured
logger. Delegation code never logs secret values; errors name the vault
path, not the content.

## What this means when you install

- Read the approval dialog: it is the complete list of what the plugin can
  ever do. There are no implicit grants.
- Updates that request more are blocked until you re-approve
  (see [Plugins](plugins.md)).
- Schema access is namespaced too: plugin migrations can only touch
  `plugin_<id>_*` tables in a dedicated database — never core tables.
- If something looks wrong later, the per-plugin audit files show exactly
  which capabilities were exercised, when, and what was refused.
