# Resource Ownership and Lifecycle

Soulacy treats ownership as a storage contract, not a UI filter. The canonical,
machine-checked inventory lives in `internal/ownership/catalog.go`. Any pull
request that introduces a SQL table without adding its ownership, scope,
lifecycle policy, and isolation test fails CI.

## Ownership classes

| Class | Meaning | Required boundary |
|---|---|---|
| Platform-global | Control-plane metadata with no tenant payload | Platform operator policy |
| Organization-owned | Shared deliberately by an organization | `organization_id` or a constrained parent relation |
| Workspace-owned | Visible only inside one workspace | Required `workspace_id` and composite uniqueness |
| User-private | Private to a user inside a workspace | Required `workspace_id` plus user/principal ownership |
| Ephemeral | Bounded process/request data that is not durable | Verified request context; no persistence |

An ID or name is never a tenant boundary. Human names, agent IDs, session IDs,
and external identifiers may collide across workspaces, so unique constraints
for workspace data include `workspace_id`.

## Legacy stores

The catalog distinguishes `scoped` persistence from `personal-only`
persistence. A personal-only table remains valid for backwards-compatible
Personal deployments, but it is not eligible for Team/Scale requests. The
resource-isolation stories migrate these entries individually and add
cross-workspace negative tests before changing their state to `scoped`.

This explicit fail-closed state prevents an unfinished migration from becoming
an accidental shared database. `ownership.MultiUserBlockers()` is the single
machine-readable list used by deployment readiness as the isolation work is
completed.

## Lifecycle contract

Every resource records four behaviors in the catalog:

- **Retention:** how long the resource remains.
- **Export:** what an authorized owner can retrieve; plaintext secrets are
  never exportable.
- **Deletion:** the owning boundary and cascade/retention behavior.
- **Backup:** the database/object/vault material required for a valid restore.

The catalog covers agents and definitions, sessions and messages, events,
memory, costs, credentials and secrets, schedules and approvals, knowledge and
vectors, artifacts and workboard data, Studio drafts/traces/learning, skills,
MCP servers, plugins, registries, channels, webhooks, shares, API keys, audit
records, tenancy metadata, durable queues, and schema metadata.
