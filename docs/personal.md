# About Soulacy Personal

Soulacy Personal is open-source and self-hosted. You run the gateway on your
machine or in your own cloud account and connect to it from the web workspace
or iPhone companion.

## What you operate

- **Gateway:** the service that loads agents, executes permitted tools, and keeps
  records. It must be running and reachable when you use a client.
- **Model connection:** a local model runtime or an approved cloud provider.
  Self-hosting the gateway does not automatically make model requests offline.
- **Workspace and storage:** agent definitions, memory, output, and database
  files. Keep backups and protect access.
- **Clients:** the browser workspace and optional iPhone companion. A client
  does not replace the gateway.

You control configuration, backups, updates, provider connections, and network
access. Start with [Quick Start](getting-started/quickstart.md) or
[self-hosting in your cloud account](deployment/cloud.md).

## What these guides assume

These docs follow the public Personal repository's `main` branch. An installed
gateway or iPhone build may be older, so check versions before expecting a new
screen. See [recent updates](recent-updates.md) and
[safe upgrades](deployment/upgrades.md).

The [footprint guide](deployment/footprint.md) describes the gateway separately
from models, databases, and tool environments. Hosting, model providers, and
external services may cost money even though Personal is Apache-2.0 licensed.

Before sending sensitive data, review where the configured model and tools send
it. Use fictional examples while learning, keep gateway keys and pairing codes
private, and grant only the tools needed for a task.
