# Security Posture

Security settings are validated against Soulacy's runtime schema. Unknown keys
fail startup rather than silently creating a false control:

```yaml
server:
  allow_unauthenticated: false
runtime:
  allow_system_agents: []
  filesystem_roots:
    - /var/lib/soulacy/workspace
  ssrf_protection: true
  sandbox:
    enabled: true
    mode: docker
security:
  intent_gate: deny
```

`runtime.allow_system_tools` is not an alias; use the explicit
`allow_system_agents` ID list. Production should leave the list empty unless a
reviewed agent genuinely needs privileged tools.

Soulacy is designed first as a self-hosted, single-operator or same-trust-team
agent runtime. It can run powerful tools on the host, so treat any user who can
edit agent definitions, configure channels, or message a tool-enabled shared
agent as inside that trust boundary.

## Supported Trust Model

Recommended deployments:

- one operator on a laptop or workstation,
- one household or team that shares a trust boundary,
- one dedicated VPS or host for one organization or project.

Not recommended without additional isolation:

- hostile multi-tenant hosting,
- mutually untrusted users sharing one gateway,
- public bots with broad tool access,
- shared agents connected to personal credentials or sensitive host files.

For adversarial or mixed-trust use, run separate Soulacy gateways under separate
OS users, containers, VMs, or hosts, and use separate credentials.

## Gateway Authentication

Set `server.api_key` before binding to a non-loopback address.

```yaml
server:
  host: "127.0.0.1"
  port: 1947
  api_key: "sy_replace_with_a_long_random_secret"
```

When `server.api_key` is empty and the server binds to a non-loopback host,
startup fails unless `server.allow_unauthenticated: true` explicitly accepts
the risk.

## Tool Execution Boundary

Team and Scale never run tenant Python in the gateway process. They require the
remote worker executor with a hardened OCI runtime and a digest-pinned,
signature-verified execution image. Privileged built-ins run in a new
read-only, capability-free, unnetworked container per call. Startup refuses a
multi-user configuration that does not provide these boundaries.

Personal mode can instead wrap local subprocesses with the hidden
`soulacy __exec-sandbox` runner, which applies resource limits for CPU, memory,
open files, and single-file output size. This compatibility guard limits
resource exhaustion but is not a filesystem, network, user, or kernel
isolation boundary.

Recommended hardening:

```yaml
runtime:
  allow_system_agents: []
  filesystem_roots:
    - /var/lib/soulacy/workspace
  allowed_tool_dirs:
    - "/opt/soulacy/tools"
  sandbox:
    enabled: true
    mode: docker
    image: registry.example/soulacy-execution@sha256:<digest>
    container_runtime: runsc
    require_signed_image: true
    cpu_seconds: 30
    memory_mb: 512
    open_files: 256
    file_size_mb: 64
```

Use `allowed_tool_dirs` to prevent `python_file` entries from pointing at
unexpected host paths.

## Python Executor Backends

The `executor` block chooses where Python tools and Studio Python blocks run.

```yaml
executor:
  backend: process   # personal only: process | pool | docker | ssh
  workers: 4         # pool only
```

Use `process` for the simplest local setup. Use `pool` when you want lower
latency on trusted local workloads.

Use Docker when you want each Python call in a short-lived container:

```yaml
executor:
  backend: docker
  docker_image: python:3.12-slim
  docker_network: none
```

Use SSH when Python should run on another machine:

```yaml
executor:
  backend: ssh
  ssh_host: worker.example.com
  ssh_user: soulacy
  ssh_python_bin: python3
  ssh_identity: /Users/me/.ssh/soulacy_worker
```

Docker and SSH execute the same Python harness as the local backend, so workflow
behavior stays consistent. They do not automatically copy local files or host
packages; install needed libraries in the image or remote environment.

Team and Scale require the `worker` backend. The gateway publishes jobs to
NATS and the separate `soulacy-worker` binary executes them in a signed,
digest-pinned image under gVisor:

```yaml
executor:
  backend: worker
  docker_image: registry.example/soulacy-execution@sha256:<digest>
  docker_network: none
  docker_runtime: runsc
  require_signed_image: true
  cosign_key: /etc/soulacy/execution-image.pub
```

The worker verifies Docker, `runsc`, and the Cosign signature before it
subscribes for jobs. Any readiness failure exits the worker; there is no local
process fallback.

## System Tools (SEC-3)

The OS-level built-ins are split into two partitions:

- **SAFE** (read-only, always available on the local `http` channel):
  `read_file`, `list_dir`, `find_files`, `fetch_url`, `http_request`,
  `env_get`, `sys_info`. These require no special grant — an agent reachable
  on the local web channel can use them (suppress them with `builtins: []`).
- **SYSTEM** (privileged): `shell_exec`, `run_script`, `install_library`,
  `write_file`, `download_file`. These can mutate the host or run arbitrary
  code, so they require a **double opt-in**:

  1. The agent ID appears in `runtime.allow_system_agents` in `config.yaml`
     (server permit; the list is empty by default).
  2. `capabilities: [system]` in the agent's `SOUL.yaml`. The legacy
     `system_tools: true` flag is honoured as an alias.

Both gates must pass, and the request must arrive on the local `http` channel
(bot channels never receive system tools). The gateway logs which agents hold
the `system` capability at startup.

Keep the `system` capability off for agents reachable by shared or public
channels. When system tools are necessary, add confirmations:

```yaml
capabilities: [system]   # (or legacy: system_tools: true)
confirm_tools:
  - shell_exec
  - write_file
```

### Tool environment allowlist (SEC-5)

Python tool subprocesses no longer inherit the gateway's full environment.
They receive only a base allowlist — `PATH`, `HOME`, `LANG`, `TMPDIR` — plus
any variable **names** you declare per agent:

```yaml
env:
  - GITHUB_TOKEN
  - MY_SERVICE_URL
```

Gateway secrets (e.g. `ANTHROPIC_API_KEY`) are NOT visible to tool code unless
explicitly listed. Values are read from the gateway's own environment at spawn
time; names with no value are skipped.

### Filesystem roots and SSRF

`runtime.filesystem_roots` is the canonical allowlist for host file tools.
Relative paths resolve from its first entry; traversal and symlink escapes are
rejected. Keep tool source locations in `allowed_tool_dirs` and writable agent
data in `filesystem_roots` rather than granting a broad parent directory.

With `runtime.ssrf_protection: true`, model-controlled HTTP destinations are
checked before connecting and on every redirect. Metadata and link-local
destinations remain blocked. Use `runtime.allow_private_hosts` only for narrow,
reviewed internal services.

## MCP and Built-in Allowlists

Prefer explicit allowlists for production agents:

```yaml
builtins:
  - web_search
  - kb_search
mcp_servers:
  - github
mcp_tools:
  - mcp__github__search_repositories
```

Use `builtins: []` or `mcp_servers: []` for agents that should not receive those
tools.

## Channel Exposure

Channel messages are untrusted input. For shared channels:

- restrict allowed users or groups where adapters support it,
- avoid broad MCP and system-tool access,
- use dedicated credentials for the bot,
- keep personal accounts and private files out of that runtime.

WhatsApp webhook requests are authenticated by Meta's signature verification.
They are intentionally not gated by `server.api_key`, because Meta cannot send
that header.

## Practical Baseline

For a conservative production baseline:

```yaml
server:
  host: "127.0.0.1"
  api_key: "sy_replace_with_a_long_random_secret"

runtime:
  allow_system_agents: []
  filesystem_roots:
    - /var/lib/soulacy/workspace
  allowed_tool_dirs:
    - "/opt/soulacy/tools"
  sandbox:
    enabled: true
```

Then enable only the providers, channels, built-ins, MCP servers, and agent
tools needed for each deployment.
