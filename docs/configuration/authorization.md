# Authorization policy

Soulacy authorizes every Team and Scale request from the active workspace
membership resolved from PostgreSQL. A role copied into a JWT is descriptive,
not authoritative. API-key scopes narrow that membership role; they never add
permission.

## Default roles

| Capability | Owner | Admin | Developer | Operator | Viewer |
|---|---:|---:|---:|---:|---:|
| Workspace policy, RBAC, and configuration | Full | Full | — | Read config | — |
| Membership lifecycle | Full | Up to admin | — | — | — |
| Agents | Full | Full | Build, delete, enable | Read and enable | Read |
| Chat | Read/write/chat | Read/chat | Read/write/chat | Read/chat | Read history only |
| Memory | Read/write/delete | Read/delete | Read/write/delete | Read/delete | Read |
| Channels | Full | Full | Read | Read/enable | Read |
| Providers | Read/write | Read/write | Read | Read | Read |
| Skills | Full | Read | Read/write | Read | Read |
| MCP servers | Full | Full | Read | Read | Read |
| Knowledge | Full | Full | Full | Read/write | Read |
| Studio builder | Read/write | Write | Read/write | — | — |
| Templates | Full | Read/write | Read/write | Read | Read |
| Schedules | Full | Read/write | Read/write | Read/write | Read |
| Logs | Read | Read | Read | Read | Read |
| Metrics | Read/write | Read | Read | — | — |
| Secrets | List/set/delete | List/set/delete | — | — | — |
| Agent credentials | Full, including reveal | Full, including reveal | Manage, no reveal | Manage, no reveal | — |

“Full” means all actions defined for that resource. Membership administration
uses the separate hierarchy documented under [Workspace members](workspace-members.md).

Developers own authoring: they use Studio, edit agent definitions and templates,
and test their work. Operators own runtime supervision: they chat with and
enable published agents, manage schedules and delivery-channel state, and
approve guarded production actions. Removing Studio from the operator role is
intentional separation of duties; an operator cannot silently change the code
or prompt of the workload they are supervising.

## Object grants

Agent grants are keyed by workspace, role, and agent ID. An exact agent rule is
evaluated before a wildcard rule. Ordinary rules are allow-lists that can only
narrow the membership role. Only a workspace owner can deliberately mark an
object rule as elevated; without that explicit owner elevation, a grant cannot
turn a denied role action into an allowed one.

Decision order is:

1. Verify authentication and resolve the active workspace membership.
2. Apply token scopes as a narrowing boundary.
3. Evaluate the role's default resource/action permission.
4. Apply the workspace-specific exact or wildcard object rule.
5. Deny if identity, membership, policy, or authorization storage is unavailable.

WebSocket events and session history use the same workspace and object rules as
HTTP routes. Workspace administrators can audit sessions in their own workspace
but cannot subscribe to another workspace's event stream.

## Fail-closed behavior

Personal mode retains its local, backwards-compatible static-policy fallback.
Team and Scale do not: a missing RBAC manager, an authorization database error,
an unresolved membership, or an unknown role returns an error or denial instead
of silently granting access.
