# Deployment Modes

Soulacy makes its operating model explicit with `deployment.mode`. Existing
installations and new local installs default to `personal`, so upgrading does
not silently introduce infrastructure dependencies.

| Mode | Intended use | Required infrastructure |
|------|--------------|-------------------------|
| `personal` | One operator on one gateway | Embedded stores and process execution are allowed |
| `team` | Multiple authenticated users in one organization | JWT auth, PostgreSQL, Docker execution and sandboxing |
| `scale` | Multiple gateway/worker instances | Everything in Team, plus NATS/external queue and shared artifact storage |

Choose a mode during `sy setup` or change an existing installation with:

```bash
sy onboard
sy doctor
```

The wizard writes configuration; it does not provision PostgreSQL, Docker,
NATS, or object storage. `sy doctor` reports the active mode, every unmet
prerequisite, and a remediation. Startup refuses unsafe Team or Scale
configuration rather than silently falling back to Personal behavior.

## Personal

```yaml
deployment:
  mode: personal
```

This is the backwards-compatible, zero-dependency mode. It is suitable for a
trusted operator using the local GUI and CLI. Empty or omitted `mode` also
means `personal`.

## Team

```yaml
deployment:
  mode: team

server:
  api_key: "sy_bootstrap_administration_key"
  allow_unauthenticated: false

auth:
  mode: jwt
  jwt_secret: "replace-with-a-stable-secret-at-least-32-characters"

storage:
  backend: postgres
  postgres_dsn: "postgres://soulacy:password@db:5432/soulacy"

executor:
  backend: docker

runtime:
  sandbox:
    enabled: true
    mode: docker
```

The bootstrap API key is an administrative recovery credential, not a shared
end-user password. Normal users authenticate with short-lived JWTs or the
configured OIDC provider.

On startup, Team mode creates the multi-user catalog in the configured
PostgreSQL database. The catalog stores organizations, workspaces, users,
external identities, workspace-specific memberships, invitations, service
accounts, and hashed credential records. Tenant mutations and their
before/after state are committed to an append-only audit table in the same
database transaction. A database or catalog bootstrap failure is fatal; Team
mode never falls back to the Personal tenant.

Workspace selection is not authorization. A signed workspace claim or the
`X-Soulacy-Workspace` request header selects a workspace, then Soulacy resolves
the authenticated subject's active membership from PostgreSQL. The stored
membership role is authoritative, so a user can be an administrator in one
workspace and a viewer in another. Suspending or deleting that membership
blocks the next request without waiting for a token to expire.

## Scale

Scale adds distributed work coordination and a shared artifact location:

```yaml
deployment:
  mode: scale
  shared_artifact_store: "s3://company-soulacy/prod"

queue:
  backend: nats
  nats_url: "nats://nats:4222"
```

`shared_artifact_store` must name storage reachable by every gateway and
worker. Queue consumers must use durable delivery; the in-memory queue is not
accepted in Scale mode.

## Emergency unsafe acknowledgement

An operator can deliberately bypass mode prerequisite validation during an
emergency:

```yaml
deployment:
  mode: team
  acknowledgements:
    - unsafe_multi_user_prerequisites
```

This is not a compatibility switch. It weakens the deployment boundary and
`sy doctor` continues to show a warning until the acknowledgement is removed.
Prefer returning to `personal` when the deployment is genuinely single-user.
