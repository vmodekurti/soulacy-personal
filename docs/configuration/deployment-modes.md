# Deployment Modes

Soulacy makes its operating model explicit with `deployment.mode`. Existing
installations and new local installs default to `personal`, so upgrading does
not silently introduce infrastructure dependencies.

| Mode | Intended use | Required infrastructure |
|------|--------------|-------------------------|
| `personal` | One operator on one gateway | Embedded stores and process execution are allowed |
| `team` | Multiple authenticated users in one organization | JWT auth, PostgreSQL, external KMS, NATS, and signed gVisor execution workers |
| `scale` | Multiple gateway/worker instances | Everything in Team, plus replicated gateways and shared artifact storage |

Choose a mode during `sy setup` or change an existing installation with:

```bash
sy onboard
sy doctor
```

The wizard writes configuration; it does not provision PostgreSQL, the
execution-worker fleet, NATS, or object storage. `sy doctor` reports the active mode, every unmet
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
  backend: worker
  docker_image: "registry.example/soulacy-execution@sha256:<digest>"
  docker_network: none
  docker_runtime: runsc
  require_signed_image: true
  cosign_key: /etc/soulacy/execution-image.pub

queue:
  backend: nats
  nats_url: "tls://nats.internal:4222"
  nats_credentials: "/var/run/secrets/nats/soulacy.creds"
  channel_ingress_subject: "soulacy.channels.inbound"

credentials:
  kms_provider: awskms
  aws_kms_key_id: "alias/soulacy-production"

runtime:
  sandbox:
    enabled: true
    mode: docker
    image: "registry.example/soulacy-execution@sha256:<digest>"
    container_runtime: runsc
    require_signed_image: true
    cosign_key: /etc/soulacy/execution-image.pub
```

The bootstrap API key is an administrative recovery credential, not a shared
end-user password. Normal users authenticate with short-lived JWTs or the
configured OIDC provider.

Use `/admin/setup` once to bootstrap a new catalog, then use `/admin` for
normal deployment administration and tenant provisioning. These control-plane
routes do not grant the operator workspace membership. See
[Platform administration](platform-administration.md).

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

Each workspace may activate a different OIDC provider through its one-time
setup link. The provider and issuer are locked after validation so an in-place
configuration edit cannot reinterpret existing external identities. See
[Workspace identity and login](workspace-identity.md).

## Scale

Scale adds replicated gateways and a shared artifact location. Team and Scale
both execute tenant code on out-of-process workers; a gateway never starts
tenant Python or a tenant shell:

```yaml
deployment:
  mode: scale
  shared_artifact_store: "s3://company-soulacy/prod"

```

`shared_artifact_store` must name storage reachable by every gateway. Soulacy
accepts `s3://bucket/prefix` or an absolute `file:///shared/mount` URL, uploads
completed chat and workboard artifacts there, and deletes the workspace object
prefix during final erasure. Queue consumers must use durable delivery; the
in-memory queue is not accepted in Scale mode.

Scale gateways also share approval decisions and chat cancellation signals in
PostgreSQL. Live events fan out through NATS, while reconnect cursors replay
from workspace-scoped PostgreSQL event IDs, so a client may reconnect through
a different gateway without losing its run stream.

## When the gateway itself runs in Docker

Containerizing the gateway does not create an execution boundary. A container
cannot launch isolated workloads unless it can reach another container runtime.
Do not solve that by mounting `/var/run/docker.sock` into the gateway: Docker's
API can mount host files and create privileged containers, so possession of the
socket is effectively control of the host.

Personal Compose runs the gateway without a runtime socket. Team and Scale use
a separate `soulacy-worker` node pool. Workers may control the runtime on their
dedicated execution nodes, but neither the gateway nor any workload container
receives that socket. The worker and gateway mount the same encrypted workspace
filesystem at the same absolute path; each job is narrowed to its stamped
workspace before the worker creates the container.

Gateway readiness performs a real queue round trip to an active worker. A
healthy NATS server with zero consumers is therefore `not_ready`, not a
partially functioning deployment.

## Emergency unsafe acknowledgement

An operator can deliberately bypass mode prerequisite validation during an
emergency:

```yaml
deployment:
  mode: team
  acknowledgements:
    - unsafe_multi_user_prerequisites
```

This is not a compatibility switch. It may temporarily waive availability
infrastructure such as PostgreSQL or KMS while an operator performs recovery,
and `sy doctor` continues to show a warning until it is removed. It can never
waive JWT authentication or the execution boundary: the worker backend,
hardened runtime, signed digest-pinned images, network-none policy, and Docker
sandbox remain mandatory in Team and Scale.
Prefer returning to `personal` when the deployment is genuinely single-user.
