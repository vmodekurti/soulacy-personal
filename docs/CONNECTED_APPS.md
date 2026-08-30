# Connected Apps

Soulacy exposes narrowly scoped SaaS actions to agents through hosted MCP
brokers. Connections belong to one workspace, may be changed only by a
workspace owner or admin in Team and Scale, and are granted to agents tool by
tool in Studio.

Authenticated publisher and subscription sessions use a separate user-first
scope model. See [Authenticated Website Connections](agents/authenticated-connections.md)
for one-time browser sign-in, scheduled replay, reauthentication, and the
workspace service-account option.

## Composio

Create a Composio session with MCP enabled and an exact tool preset. In
**Connected Apps → Composio**, paste `session.mcp.url` and its secret header.
Soulacy stores the header in the encrypted workspace vault and registers the
remote server as `composio`.

Discovered tools appear as `mcp__composio__<tool>`. Soulacy never advertises or
executes Composio remote-workbench, remote-bash, or shell-execution tools, even
if the upstream session exposes them. Restrict the session at Composio too.

## Nango

Enable reviewed action functions for a Nango integration and authorize the
user or organization. In **Connected Apps → Nango**, enter the MCP endpoint
(`https://api.nango.dev/mcp`, or a self-hosted equivalent), environment secret
key, connection ID, and integration ID.

Soulacy sends the headers required by Nango's hosted Streamable HTTP MCP
server. Every value is encrypted in the workspace vault and is never returned
by the API. Enabled actions appear as `mcp__nango__<action>` tools in Studio.
Keep write actions idempotent because a retry may repeat an operation.

## Structured Reasoning

Soulacy includes `structured_reasoning`, a native, dependency-free counterpart
to the Sequential Thinking MCP server. It supports ordered checkpoints,
revision, and branching in Personal, Team, and Scale without an extra Node.js
process. It records concise conclusions and evidence rather than private
chain-of-thought. Grant it from Studio when a complex task needs an explicit
reasoning scaffold.

## Security model

1. A workspace administrator establishes the external connection.
2. Credentials and connection selectors go to the workspace vault.
3. The MCP client discovers tools from the selected connection only.
4. Studio grants individual namespaced tools to individual agents.
5. Runtime policy checks the grant again before every call.
6. Removing a connection invalidates the workspace MCP client immediately.

Viewer and developer roles can inspect integrations when their MCP read
permission permits it, but cannot create, update, or remove connections.
