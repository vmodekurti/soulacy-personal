# Admin API

Administrative endpoints require the corresponding config-level RBAC action.
Use an admin API key or JWT unless your policy grants a narrower role.

## Platform control plane

Team and Scale expose tenant lifecycle metadata through deployment-key-only
routes. These handlers never resolve a workspace and never return tenant-owned
agents, runs, conversations, files, knowledge, or secrets.

```http
GET /api/v1/admin/platform/overview
GET /api/v1/admin/platform/organizations
Authorization: Bearer <deployment-api-key>
```

Provision an organization, its first workspace, and designated owner:

```http
POST /api/v1/admin/platform/organizations
Authorization: Bearer <deployment-api-key>
Content-Type: application/json

{
  "organization_name": "Acme",
  "workspace_name": "Production",
  "workspace_slug": "acme-production",
  "owner_display_name": "Avery Admin",
  "owner_email": "avery@example.com",
  "organization_logo": "data:image/png;base64,...",
  "workspace_logo": "data:image/png;base64,..."
}
```

Provision another workspace in an existing organization:

```http
POST /api/v1/admin/platform/organizations/{organization-id}/workspaces
Authorization: Bearer <deployment-api-key>
Content-Type: application/json

{
  "workspace_name": "Operations",
  "workspace_slug": "acme-operations",
  "owner_display_name": "Morgan Owner",
  "owner_email": "morgan@example.com",
  "workspace_logo": "data:image/webp;base64,..."
}
```

Change an existing workspace's public sign-in address:

```http
PATCH /api/v1/admin/platform/workspaces/{workspace-id}/address
Authorization: Bearer <deployment-api-key>
Content-Type: application/json

{
  "slug": "acme-support"
}
```

The address must be unique across the deployment and contain 3–48 lowercase
letters, numbers, or hyphens. The response includes the updated workspace and
its `/w/{address}` login URL.

Both provisioning responses contain a one-time `setup_token`. The GUI turns
it into `/w/{workspace-address}/setup?token=...`; deliver it securely to the
workspace administrator. Provisioning does not make the deployment operator a
workspace member. See
[Platform administration](../configuration/platform-administration.md).

Suspend or reactivate an organization:

```http
PATCH /api/v1/admin/platform/organizations/{organization-id}/status
Authorization: Bearer <deployment-api-key>
Content-Type: application/json

{
  "status": "suspended",
  "reason": "Security review INC-2048",
  "confirm_name": "Acme"
}
```

Suspend or reactivate one workspace:

```http
PATCH /api/v1/admin/platform/workspaces/{workspace-id}/status
Authorization: Bearer <deployment-api-key>
Content-Type: application/json

{
  "status": "suspended",
  "reason": "Customer-requested maintenance hold",
  "confirm_name": "Production"
}
```

Valid states for these control-plane endpoints are `active` and `suspended`.
Suspension requires both `reason` and an exact `confirm_name`; reactivation
does not. Organization suspension is evaluated as an outer hold and therefore
does not overwrite the lifecycle state of its child workspaces.

## Health and readiness

```http
GET /api/v1/health
Authorization: Bearer <token>
```

The health response reports status, version, timestamp, dependency health, and
the request ID. Use the deeper readiness views before exposing a deployment:

```http
GET /api/v1/readiness
GET /api/v1/security/readiness
Authorization: Bearer <token>
```

## Restart the gateway

```http
POST /api/v1/admin/restart
Authorization: Bearer <admin-token>
```

The gateway starts a replacement process with the same executable and
arguments, returns `202 Accepted`, then exits:

```json
{
  "ok": true,
  "message": "Restart requested. A replacement gateway process is starting."
}
```

For systemd deployments, verify that the service has an explicit config path
and workspace before relying on an in-place restart. See
[Upgrades](../deployment/upgrades.md).

## Gateway logs and support bundle

```http
GET /api/v1/logs?lines=500&filter=error
GET /api/v1/support/bundle
Authorization: Bearer <deployment-admin-key>
```

In Team and Scale, these are deployment-level diagnostics and accept only the
deployment administrator credential. Workspace owners and other members use
the workspace-scoped run ledger and `GET /api/v1/agents/{id}/actions`; they
cannot tail the shared process log or download its support bundle.

## Administrative audit log

```http
GET /api/v1/admin/audit?limit=100
Authorization: Bearer <admin-token>
```

`limit` defaults to 100 and is capped at 1,000. The response contains
newest-first administrative events from the durable action log. If durable
action logging is unavailable, the endpoint returns `503 Service Unavailable`.

## Dead-letter queue

Failed executor jobs are retained for diagnosis when a DLQ store is configured.

### List entries

```http
GET /api/v1/admin/dlq?queue={agent-or-queue-id}
Authorization: Bearer <admin-token>
```

The optional `queue` parameter filters results. The response contains `items`
and `count`; each item includes its ID, queue, original payload bytes, error,
attempt count, and timestamps.

### Inspect one entry

```http
GET /api/v1/admin/dlq/{id}
Authorization: Bearer <admin-token>
```

### Delete one entry

```http
DELETE /api/v1/admin/dlq/{id}
Authorization: Bearer <admin-token>
```

```json
{
  "status": "deleted",
  "id": "<id>"
}
```

Deletion is permanent. The gateway does not expose an automatic DLQ retry
endpoint; diagnose the failure and replay the originating request explicitly.

## Managed API keys

Managed-key creation, listing, validation, and revocation are documented in
the [Auth API](auth.md).

## Registries and plugins

Marketplace-style discovery is exposed through `/api/v1/registries/*` and
plugin lifecycle operations through `/api/v1/plugins/*`; they are not admin
marketplace endpoints. Use the [Registries API](registries.md) and
[Plugins guide](../extend/plugins.md) for those surfaces.
