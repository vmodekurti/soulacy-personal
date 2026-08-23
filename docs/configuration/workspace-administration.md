# Workspace administration

Workspace owners manage customer-owned controls from **Workspace settings** in
the Soulacy sidebar. The page is available only to the active workspace's
owner and never grants access to deployment operations.

!!! note "Separate responsibility"
    A deployment administrator provisions and suspends tenant boundaries, but
    does not become a workspace member. A workspace owner manages members,
    limits, credentials, exports, and deletion for only their active workspace.

## Limits and retention

An owner can set daily and monthly spend limits, daily token limits, concurrent
run limits, and retention windows for conversations, action events, and audit
records. Zero or an empty duration means “inherit the deployment setting.”

Workspace values can only tighten the deployment administrator's ceiling. The
screen shows both the stored request and the effective value so an owner can
see when a deployment limit is lower. Retention values use Go duration syntax,
such as `720h` for 30 days.

## Workspace model and search configuration

The **Config** screen in Team and Scale deployments is workspace-scoped. An
owner or administrator can select the default, Chat, and Studio provider/model
from the provider catalog allowed by the deployment. Studio runtime intent,
generate experience, and per-build token/cost limits are also workspace-owned.
The same screen exposes daily/monthly LLM ceilings and concurrency limits.

Web-search provider, timeout, and API key are workspace settings. The key is
stored in the encrypted workspace vault; the settings database records only
that a key exists. These values take effect without a gateway restart and are
resolved from the verified workspace identity on each run.

The deployment administrator still controls which providers and models are
available. A workspace can select or tighten within that catalog, but cannot
register a host-wide provider, raise deployment ceilings, or change another
workspace.

## Workspace MCP servers

The **MCP** screen creates servers in the active workspace's durable registry.
Credentials are diverted into that workspace's vault, and the runtime pool is
invalidated only for that workspace so changes take effect immediately. The
deployment MCP template remains a separate platform-admin concern.

## Automation credentials

Workspace owners can issue personal automation credentials for CI jobs and
other non-interactive clients. Credentials are bound to the active workspace,
inherit the owner's role, require explicit `resource:action` scopes, and expire
after 90 days unless the deployment imposes a different policy.

The plaintext secret is displayed once. Copy it into a secret manager before
leaving the page. Rotation immediately revokes the previous secret and displays
one replacement once. Revocation is immediate and cannot be undone.

Browser login never requires this credential. Human workspace users continue
to authenticate through the workspace's locked OIDC provider.

## Audit trail

The audit tab searches durable administrative events for the active workspace.
Use an actor email or subject, action, resource, target, workspace identifier,
or request ID to narrow an investigation. **Export visible** downloads the
currently filtered records as JSON.

Audit reads are themselves recorded. The workspace trail does not expose
another workspace's events or deployment-wide process logs.

## Workspace export

An owner can create an asynchronous, workspace-scoped export. Completed
archives are checksummed, expire automatically, and can be downloaded only by
an owner of the same workspace. The archive inventory follows Soulacy's data
ownership catalog so newly supported workspace resource classes cannot be
silently omitted.

## Recoverable deletion

Deletion is deliberately staged:

1. Reauthenticate when prompted.
2. Enter a reason and choose a recovery window of at least 24 hours.
3. Type the workspace name exactly.
4. Soulacy pauses writes and schedules the purge.
5. An owner can cancel before the recovery deadline and restore access.

After the recovery window closes, the purge begins and can no longer be
cancelled. Use deployment-level suspension for an incident or contractual hold;
use deletion only when the workspace and its data should be removed.

See also [Workspace members](workspace-members.md),
[Workspace identity](workspace-identity.md), and
[Platform administration](platform-administration.md).
