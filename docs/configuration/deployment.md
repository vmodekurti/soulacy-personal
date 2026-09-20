# What your deployment can do

Soulacy runs on a laptop that can install anything, and in a container on a
platform where you have no shell at all. Those deployments cannot do the same
things, and the difference is worth knowing before you follow an install guide
written for the other one.

The gateway reports its own limits. Open the **MCP Servers** page, or ask the
API:

```bash
curl -s -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:18789/api/v1/doctor | jq .deployment
```

Every limit comes with the way around it. A capability that is unavailable and
offers no alternative is a bug in this report, not a fact about your
deployment.

## What it tells you

| Capability | What it means |
| --- | --- |
| **Agents can run shell commands** | whether any agent holds the `system` grant |
| **System packages can be installed** | whether the gateway runs as root |
| **Workspace survives a redeploy** | where installs land, and whether it is writable |
| **Node / Python MCP servers** | present, and *at what version* |
| **Browser automation** | local browser, remote browser, or neither |

The version matters more than it looks. An MCP server that needs Node 20 on a
Node 18 runtime exits before the handshake, and the gateway sees only a closed
pipe: `initialize: stdio transport closed before response`, which says nothing
about Node.

## Installing things without a shell

You do not need one. Paste the repository into the **MCP Servers** page, or ask
the System agent to install it. Soulacy inspects the README and manifests first,
then chooses a hosted endpoint, gateway process, connected device, or companion
service. `package_install` is used only for a compatible gateway process; it
installs into the persistent workspace, registers the server, verifies the
result, and asks for approval before changing anything.

What a shell would give you beyond that is mostly the ability to break things
in ways nobody can undo from the GUI.

## Shell on a cloud platform

`runtime.allow_system_agents` hands an agent `shell_exec`, `run_script`,
`write_file` and friends. On a machine you own, that is your call to make.

On Railway, Fly.io, Render, Cloud Run or Heroku it is **refused**, and the
deployment report says so. Three things are true there at once: the gateway is
usually reachable from the internet, you have no shell of your own to repair
what an agent breaks, and the container is rebuilt from an image you may not
control.

A container you run yourself — Docker, Kubernetes — keeps the decision. The
line is whether you can get in and undo a mistake, not whether the machine
happens to be in a data centre.

## Things that need root

Some dependencies are system packages, and no runtime trick installs them
without root. When the report says a system package is missing, the answer is
always the image: add it to the Dockerfile and redeploy. The report says that
rather than suggesting a command that cannot work.

The exception is anything that only needs to be *found* rather than installed —
see [browser automation](../agents/browser-automation.md), which turns ~30MB of image
weight into a download you make only if you need it.
