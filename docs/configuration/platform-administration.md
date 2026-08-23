# Platform administration

Team and Scale deployments have a dedicated control plane at `/admin`. It is
authenticated with the deployment API key and is intentionally separate from
workspace login.

!!! important "Not a workspace super-user"
    The deployment administrator can operate the service and provision tenant
    boundaries, but cannot read workspace agents, runs, conversations,
    knowledge, files, memories, or secrets. Workspace access always requires
    an explicit membership granted inside that workspace.

## Routes

| Route | Purpose | When to use it |
|---|---|---|
| `/` | Public access page | Choose workspace or deployment administration |
| `/admin` | Deployment control plane | Normal, repeat-use platform operations |
| `/admin/setup` | First-time bootstrap | Once, on a new Team or Scale catalog |
| `/w/{workspace-id}` | Branded workspace access | Workspace owner setup and member login |

Every public access screen includes consistent Home, Workspace login,
Deployment admin login, documentation, and [Soulacy.io](https://soulacy.io)
links. Use **Page help** for guidance specific to the current screen.

## Administrator login

Open `/admin` and enter `server.api_key` from the host configuration (or the
equivalent `SOULACY_API_KEY` environment variable). The key is a deployment
credential. Team and Scale reject it on ordinary workspace routes.

Keep the key in a password manager or deployment secret store. Do not send it
to workspace owners and do not use it as an end-user password.

## First-time bootstrap

Use `/admin/setup` only when the tenant catalog is empty. Before starting:

1. Configure PostgreSQL and the Team or Scale prerequisites.
2. Configure a stable JWT signing secret.
3. Register the exact Soulacy callback URL with the initial OIDC provider.
4. Open `/admin/setup` and authenticate with the deployment setup key.
5. Create the first organization, workspace, and owner.
6. Restart the gateway when prompted, then use `/admin` for later operations.

Bootstrap is transactional and protected by a database advisory lock. Two
concurrent setup tabs cannot create competing first owners. If the catalog is
partially initialized, setup stops instead of guessing how to repair it.

## Overview

The Overview screen reports only service-level information:

- organization, workspace, user, and active-membership counts;
- gateway version and deployment mode;
- authentication posture;
- readiness and draining state.

It does not resolve a workspace or query tenant-owned payloads.

## Provisioning

The **Organizations** screen supports two operations:

- **Create organization** creates an organization, its first workspace, and a
  designated workspace owner in one transaction.
- **Create workspace** adds an isolated workspace to an existing organization
  and designates its initial owner.

Each workspace can have a deployment-wide unique public address such as
`acme-support`. Set it while provisioning or use **Edit address** beside an
existing workspace. Addresses contain 3–48 lowercase letters, numbers, or
hyphens and produce the login URL `/w/{address}`. If omitted, Soulacy generates
a stable address from the workspace name. Changing an address takes effect
immediately, so share the new link with members.

Both operations return a one-time workspace setup link. Send that link to the
workspace administrator through a trusted channel. It expires after seven
days and can be used once. The deployment administrator does not become a
workspace member.

Organization and workspace logos may be uploaded as PNG, JPEG, or WebP data,
up to 500 KB. Logos appear on workspace access and workspace-selection
screens. Avoid embedding secrets or personal data in images.

## Suspending tenant access

The deployment administrator can suspend an organization or an individual
workspace from **Organizations**. Suspension requires a reason and typing the
tenant name, and every transition is written to the durable tenant mutation
audit.

- Suspending an organization blocks new sign-ins and writes across all of its
  workspaces. It does not rewrite the individual workspace rows.
- Suspending one workspace affects only that workspace.
- Existing sessions become read-only so members can see that the workspace is
  on hold rather than being told that their membership disappeared.
- Reactivating an organization restores only child workspaces whose individual
  state is active. A separately suspended or deleting workspace stays that
  way.
- A suspended organization cannot receive another workspace, and a suspended
  workspace cannot activate its identity provider.

Use suspension for a security incident, contractual hold, or maintenance
freeze. Use the workspace owner's recoverable deletion flow when the customer
actually wants data removed.

## Responsibility boundaries

The deployment administrator owns service availability, tenant provisioning
and suspension, deployment configuration, upgrades, shared dependencies,
capacity, raw gateway logs, support bundles, restarts, and the searchable
deployment audit trail. Backup/restore orchestration, certificate health, and
emergency session revocation remain deployment-integration responsibilities;
adding them must not create access to tenant content.

Workspace owners own members and invitations, roles, workspace branding,
workspace OIDC setup, agents, model credentials, channels, workspace policies,
exports, and recoverable deletion. Organization-wide delegated administration,
budgets, domain verification, and owner succession remain separate product
capabilities; they should not be simulated by turning the deployment operator
into a workspace member.

Team and Scale preserve the Personal-mode product inside each workspace. A
workspace therefore retains agents, Studio, Chat, learning, knowledge, queues,
workboard, delivery, automations, skills, MCP servers, plugins, provider/model
configuration, secrets, runs, browser artifacts, safe configuration, and
mobile controls. Their persistence and credentials are selected from verified
workspace context.

Only operations that affect the shared deployment are removed from workspace
navigation: raw gateway logs, gateway restart, upgrades, worker and sandbox
policy, KMS and billing infrastructure, host paths, registry credentials,
deployment provider allowlists/base endpoints and resource ceilings, and
shared catalog admission. Workspace owners may configure an approved provider
or extension for their workspace; they cannot mutate the deployment definition
or another workspace.

## Diagnostics

The Diagnostics screen shows shared dependency readiness, can tail the raw
gateway log, and can export a redacted platform snapshot. Raw gateway logs and
support bundles require the deployment administrator key because the process
is shared and its output can contain signals from multiple workspaces.

Workspace members never receive this deployment log. Their **Runs** screen and
agent action history are scoped to their active workspace and expose only the
events belonging to its agents. Start with the diagnostic snapshot before
restarting the gateway. Restarting interrupts in-flight work across every
workspace and is available only from the deployment control plane.

For command-line checks, run:

```bash
sy doctor
sy daemon status
sy daemon logs
```

## Audit trail

The **Audit trail** screen searches deployment-key activity and organization or
workspace lifecycle operations. Search accepts actor, action, resource, tenant
identifier, or request ID, and the filtered result can be exported as JSON for
an incident record. Older records are loaded with a durable cursor rather than
an offset, so concurrent activity does not skip or duplicate page boundaries.

The trail contains operational metadata, not agents, conversations, files,
secrets, or other workspace payloads. Reading it is also audited so an operator
cannot inspect the control-plane record without leaving an accountable event.

## Security boundary

There is no hidden platform support bypass. If a support engineer must inspect
workspace content, a workspace owner must invite that engineer as a normal,
least-privileged member. Remove or suspend that membership when the support
window ends; the membership audit records the change.

See also [Workspace identity](workspace-identity.md),
[Workspace members](workspace-members.md), and
[Workspace administration](workspace-administration.md), and
[Authorization policy](authorization.md).
