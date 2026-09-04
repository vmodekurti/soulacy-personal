# Admin API

Administrative endpoints require the corresponding config-level RBAC action.
Use an admin API key or JWT unless your policy grants a narrower role.

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
