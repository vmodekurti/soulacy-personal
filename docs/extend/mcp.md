# MCP Servers

Soulacy speaks the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP), so any MCP server — first-party or third-party — can expose its tools to
your agents. An MCP server might wrap a database, a SaaS API, a filesystem, or a
company-internal service; once connected, its tools appear to agents exactly like
built-in tools.

## How Soulacy uses MCP

- **As a client.** Soulacy connects to MCP servers you configure and makes their
  tools callable from an agent's `tools:` list.
- **As a server.** Soulacy can also expose selected capabilities *as* an MCP
  server for other MCP-aware clients.

## Connect an MCP client to Soulacy

Every Soulacy gateway exposes an authenticated Streamable HTTP endpoint at
`<gateway-url>/mcp`. It runs inside the gateway, so Railway and other managed
platforms do not need an interactive shell, sidecar process, or `sy mcp serve`
session.

For a deployment at `https://beta.soulacy.io`, configure the MCP client with:

```json
{
  "url": "https://beta.soulacy.io/mcp",
  "headers": {
    "Authorization": "Bearer <SOULACY_API_KEY>"
  }
}
```

The endpoint exposes enabled agents as tools, plus authenticated schedule,
Workboard, knowledge-base, and queue operations. The API key or JWT keeps its
normal Soulacy RBAC scopes; MCP does not bypass permissions. The endpoint uses
stateless JSON responses and returns `405 Method Not Allowed` for the optional
standalone SSE stream.

Clients that only support local stdio can use the CLI as a compatibility bridge:

```bash
sy --gateway https://beta.soulacy.io \
  --api-key "$SOULACY_API_KEY" mcp serve
```

## Configuring an MCP server

### Install from a repository URL

In Chat, select the built-in **System** agent and ask:

> Install the MCP server from https://github.com/owner/repository

The System agent first uses a read-only inspection tool. It reads a bounded
README excerpt and machine-readable evidence such as `server.json`, package
manifests, Dockerfiles, Compose services, container images, hosted endpoints,
runtime requirements, and persistence signals. The LLM then chooses the best
placement:

- connect to a provider-hosted MCP endpoint;
- install a self-contained Python or Node process in the Soulacy gateway;
- run a device-bound server on its device (through a connected Soulacy Runner
  when runner installation is available, or through a secured HTTP endpoint
  today); or
- deploy a stateful, containerized, or multi-service server as a companion.

Repository documentation is treated as untrusted evidence and its commands are
never copied into a shell automatically. When the gateway-process method fits,
the typed installer presents **Approve / Deny**, performs the safety scan,
installs into `mcp-servers/`, updates the live config, and verifies the command.
For a verified provider-hosted endpoint that needs no credentials, the agent
can register the endpoint through a typed, approval-gated action. Authenticated
endpoints receive exact Secrets and MCP-page directions so credentials never
enter model tool arguments. For companion and device methods, the agent returns
concrete deployment and `sy mcp add` steps until that external endpoint exists.
If it cannot find a viable method, it must identify the files it inspected, the
blocking requirement, why the plausible placements do not fit, and what would
make the server installable.

This managed installer remains available when `runtime.allow_system_agents` is
empty. You do not need to enable arbitrary shell access just to install an MCP
server. The installer is limited to HTTPS Git URLs, preserves an explicit MCP
choice from the request, and still requires approval for every installation.

The equivalent CLI command is:

```bash
sy package install https://github.com/owner/repository --kind mcp --allow-unverified

# Run the complete installation on a remote Personal gateway host.
sy --gateway https://soul.example.com package install \
  https://github.com/owner/repository --kind mcp \
  --allow-unverified --allow-host-build
```

Raw Git repositories are unsigned, so direct CLI use requires
`--allow-unverified`. In Chat, approving the exact URL provides that explicit
consent. Repeating the request does not reinstall an already registered server.

MCP servers are declared in your Soulacy config (or contributed by a plugin).
Each server has a transport — a local subprocess over stdio, or a remote URL.

```yaml
mcp:
  servers:
    - name: filesystem
      transport: stdio
      command: ["mcp-server-filesystem", "--root", "~/soulacy-files"]
    - name: company-crm
      transport: http
      url: https://mcp.internal.example.com
      # Secrets are referenced by name from the vault, never inlined.
      auth:
        bearer_token: ${secret:CRM_MCP_TOKEN}
```

After adding a server, restart the gateway (or reload config) so the tools are
discovered.

The same definitions can be registered without editing the gateway host:

```bash
# Idempotently create or update an HTTP MCP server on a remote gateway.
sy --gateway https://soul.example.com mcp add \
  --name company-crm --transport http \
  --url https://mcp.internal.example.com/mcp \
  --header 'Authorization=Bearer ${CRM_MCP_TOKEN}'

# Personal edition only: launch a stdio server on the remote gateway host.
sy --gateway https://soul.example.com mcp add \
  --name filesystem --transport stdio \
  --command /srv/soulacy/mcp-servers/filesystem/venv/bin/mcp-server-filesystem \
  --args '--root,/srv/soulacy-files' --env 'LOG_LEVEL=info'
```

Pass `--api-key "$SOULACY_API_KEY"` when the remote gateway requires
authentication. This command runs on your computer and writes the MCP
registration through the gateway API; it does not require shell access on the
deployment.

`sy` sends remote registrations to `PUT /api/v1/mcp/own/:id`; it does not edit
the caller's configuration. Team and Scale deployments accept only HTTPS
remote MCP definitions through this endpoint and reject stdio. For a repository
that needs cloning or a build environment, use remote `sy package install`
instead; raw source builds can be limited to an administrator-approved catalog.

## Using MCP tools in an agent

Reference the discovered tool names in a SOUL.yaml `tools:` block:

```yaml
name: crm-assistant
tools:
  - company-crm.search_contacts
  - company-crm.create_note
  - filesystem.read_file
```

MCP tools carry the same **risk tiers** and policy controls as any other tool
(see [Safety Introspection](safety.md)). A privileged or network-capable MCP tool
will surface in an agent's capability tier before you bind that agent to a
channel.

## Declaring MCP requirements in templates

A template can declare the MCP servers it needs. The **Template Install Wizard**
then shows an MCP server in its readiness checklist and won't mark the template
ready until the server is reachable and its secrets are set. See
[Workflow Templates](../using/templates.md).

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Tool not found on agent | Server not connected at startup | Check the gateway log for the server name; verify `command`/`url` |
| `unauthorized` from a remote server | Missing or wrong token | Confirm the referenced secret is set in the vault |
| Stdio server exits immediately | Binary not on PATH | Use an absolute `command` path or install the server |
| Tool call hangs | Server slow or blocked | Check the server's own logs; set a timeout on the step |

See also [Common failures](../troubleshooting/common-failures.md).
