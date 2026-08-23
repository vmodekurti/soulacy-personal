# Resource Ownership and Lifecycle

Soulacy treats ownership as a storage contract, not a UI filter. The canonical,
machine-checked inventory lives in `internal/ownership/catalog.go`. Any pull
request that introduces a SQL table without adding its ownership, scope,
lifecycle policy, and isolation test fails CI.

## Personal parity invariant

Personal mode is the behavioral baseline. Team and Scale run that same product
inside a verified workspace scope and add tenancy and governance; they are not
reduced editions and must not replace working Personal features with parallel,
weaker implementations.

For a capability that is safe in Personal mode, the default ownership rule is
workspace-owned in Team and Scale. A feature may be withheld only when it
changes shared process, host, security, capacity, billing, or catalog state.
The same UI component and API semantics should be reused wherever possible;
the active scope selects the store, credentials, paths, and policy envelope.

| Surface | Personal | Team/Scale ownership |
|---|---|---|
| Agents, Studio, templates, learning, knowledge, queues, workboard | Local user deployment | Workspace-owned |
| Shared workspace Chat and agent runs | Local user deployment | Workspace-owned; conversations/messages remain user-private |
| Delivery, automations, skills, MCP instances, plugins | Local user deployment | Workspace-owned, bounded by deployment allowlists |
| Provider/model choices and credentials, secrets, safe config | Local user deployment | Workspace-owned overlays and vault entries; deployment sets allowed endpoints and ceilings |
| Browser artifacts, agent logs, mobile integration | Local user deployment | Workspace-owned and stored/read through workspace scope |
| Members, invitations, roles, OIDC binding, branding, retention | Not applicable | Workspace governance |
| Organization metadata, domains, workspace collection | Not applicable | Organization-owned |
| Gateway restart, raw host logs, upgrades, workers/sandbox, KMS, billing, registries, shared catalogs | Local operator | Platform-global control plane |

User-private data remains private inside its workspace: sessions,
conversations, messages, personal memory, Studio drafts, and user credentials
carry both `workspace_id` and user/principal ownership. A deployment
administrator is not a workspace super-user and cannot use control-plane
credentials to read tenant payloads.

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
