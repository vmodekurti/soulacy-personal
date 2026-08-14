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

## Configuring an MCP server

### Install from a repository URL

In Chat, select the built-in **System** agent and ask:

> Install the MCP server from https://github.com/owner/repository

Soulacy uses a typed installer tool and presents **Approve / Deny** before it
changes anything. After approval it detects the package runtime, performs the
safety scan, installs into `mcp-servers/`, updates the live config, and verifies
the registered command. It does not ask the model to construct shell commands.

This managed installer remains available when `runtime.allow_system_agents` is
empty. You do not need to enable arbitrary shell access just to install an MCP
server. The installer is limited to HTTPS Git URLs, preserves an explicit MCP
choice from the request, and still requires approval for every installation.

The equivalent CLI command is:

```bash
sy package install https://github.com/owner/repository --kind mcp --allow-unverified
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
