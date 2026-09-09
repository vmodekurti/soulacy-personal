# CLI Reference

Two binaries:

- **`sy`** — the CLI client. Every GUI action is available here; commands
  talk to a running gateway over its REST API.
- **`soulacy`** — the gateway server itself, plus the build tool and the
  reference package registry.

## Global `sy` flags

| Flag | Default | Description |
|------|---------|-------------|
| `--gateway` | `http://localhost:18789` (or `cli.gateway_url` / `server.port` from config) | Gateway URL |
| `--context` | `SOULACY_CONTEXT` or `cli.active_context` | Named entry under `cli.contexts` |
| `--api-key` | `server.api_key` from config | API key for gateway authentication |
| `--json` | `false` | Output raw JSON |

`SOULACY_GATEWAY` and `SOULACY_API_KEY` are the environment equivalents of
the gateway and API-key flags. Named contexts keep several targets explicit:

```yaml
cli:
  active_context: personal
  contexts:
    personal:
      gateway_url: https://soul.example.com
      api_key: ${SOULACY_PERSONAL_API_KEY}
    local:
      gateway_url: http://localhost:18789
```

Flags take precedence over environment variables, which take precedence over
the selected context and then the default local configuration.

## Local and remote execution

`sy` treats Unix sockets, `localhost`, and loopback IP addresses as local. Any
other gateway is remote. The selected target is resolved once, before a command
runs; an unreachable remote gateway never causes a fallback to local files.

Remote targets allow:

- REST-backed reads and diagnostics, including agent, channel, schedule,
  memory, skill, secret, MCP, log, and gateway status operations.
- REST-backed mutations, including agent/channel management, MCP registration,
  registry changes, and remote package installation.
- Client-only read operations such as `registry probe`, `workspace info`,
  `daemon status`, `daemon logs`, `version`, and update/launch checks. These
  describe or inspect the client host where applicable, not the remote host.
- Explicit client maintenance such as CLI update/upgrade and support-bundle
  creation. Their command names make the local effect clear; they never expose
  a remote daemon or operating-system upgrade API.

| Command type | Remote policy | Enforced guardrail |
| --- | --- | --- |
| Agent, chat, and state | Allowed | Gateway authentication and workspace RBAC |
| Secrets and credentials | Allowed, write-only values | TLS, encrypted vault, response redaction |
| Remote HTTP MCP | Allowed | HTTPS plus NetGuard public-address, DNS-pinning, and redirect checks |
| stdio host MCP | Personal only | Executable must resolve inside the managed `mcp-servers/` root; checked again at process start |
| Git source package | Explicit approval required | Static inspection, sandboxed dry-run, `allow_unverified`, and separate `allow_host_build` consent |
| Server daemon or platform upgrade | No remote API | Operate on the platform host; client self-upgrade remains explicitly local |

Commands whose current implementation would ambiguously mutate the client host
are refused for remote targets before any file is written. This includes
`setup`, `onboard`, `server start`, daemon install/start/stop/uninstall,
`workspace migrate` (except `--dry-run`), `seed-examples`, `pull`, and mutating
`voice` setup commands. Run these on the gateway host or use a corresponding
gateway API. The refusal always identifies the remote target and confirms that
no local files were changed.

---

## Getting set up

```bash
sy setup            # interactive wizard: providers, channels, writes config.yaml
sy doctor           # local diagnostics — config, dirs, Python, Ollama, gateway, MCP
sy doctor --json    # machine-readable report
```

`sy doctor` exits nonzero only on hard failures; warnings flag suspicious
but non-fatal configuration (relative `agent_dirs`, non-absolute
`runtime.python_bin`, …).

It also checks the production update path. If `updates.manifest_url` or
`SOULACY_UPDATE_MANIFEST` is configured, the doctor fetches the release manifest
and reports whether this install is current, upgradeable, or unable to reach the
manifest. Missing or unreachable update manifests are warnings so local
development is not blocked, but production workspaces should clear them.

## Managing agents

```bash
sy agent list
sy agent get support-bot
sy agent create --file ./SOUL.yaml
sy agent validate examples/agents/hello-world/SOUL.yaml
sy agent enable support-bot
sy agent disable support-bot
sy agent trigger daily-briefing      # manually fire a scheduled agent
sy agent delete old-bot

sy agent package export support-bot --out support-bot.soulacy-agent.json
sy agent package export support-bot --signing-key-file ./ed25519.hex
sy agent package inspect support-bot.soulacy-agent.json
sy agent package import support-bot.soulacy-agent.json      # imports disabled for review
sy agent package import support-bot.soulacy-agent.json --enable --overwrite
```

`sy agent validate` checks YAML fields, trigger/schedule consistency,
provider and model availability, tool paths, and MCP references — errors
return nonzero, making it CI-friendly.

Agent packages are portable `.soulacy-agent.json` bundles. They include
redacted `SOUL.yaml`, safe local tool files when available, bundled
`evals/`, `prompts/`, and `samples/` files, a setup requirements checklist,
a content checksum, and optional Ed25519 signature metadata. `inspect`
verifies package integrity and shows missing providers, channels, peer
agents, skills, files, knowledge bases, and secrets before anything is
imported.

Pull a definition from a URL or the public registry:

```bash
sy pull my-agent                                   # registry ID
sy pull org/repo                                   # GitHub shorthand (main/SOUL.yaml)
sy pull https://example.com/agents/agent.yaml      # direct URL
sy pull my-agent --dir ~/agents --force            # custom dir, overwrite
```

## Chatting & evaluating

```bash
sy chat --agent support-bot "Summarize today's tickets"
sy chat --agent support-bot --user alice "Hello!"

sy eval --agent my-agent --suite tests/smoke.json        # pass/fail report
sy eval --agent my-agent --suite tests/smoke.json --json
sy eval --agent my-agent --suite evals/golden --tag weather --repeat 3
sy eval --agent my-agent --suite evals/golden --fail-fast
```

Eval suites are JSON:
`{"name": "smoke", "cases": [{"name":"math","input":"2+2?","expected_contains":["4"]}]}`.
A failing case makes the command exit nonzero. Reports include aggregate
pass/fail/skip counts plus latency and token summaries when available.

## Channels

```bash
sy channel list
sy channel status whatsapp_web
sy channel enable telegram
sy channel disable whatsapp_web
sy channel update telegram --set trigger_phrase='!soulacy' --set ignore_groups=true
```

Each adapter also has a first-class namespace with
`status` / `enable` / `disable` / `configure`:

```bash
sy channel telegram configure --token "$TELEGRAM_BOT_TOKEN" --agent assistant
sy channel slack configure --bot-token "$SLACK_BOT_TOKEN" --app-token "$SLACK_APP_TOKEN" --agent assistant
sy channel discord configure --token "$DISCORD_BOT_TOKEN" --agent assistant --guild '1234567890'
sy channel whatsapp configure --phone-number-id "$ID" --access-token "$TOK" \
  --verify-token "$VTOK" --app-secret "$SECRET" --agent assistant
sy channel http status
```

All `configure` commands share the activation-safety flags: `--trigger`
(wake phrase), `--allow-groups`, `--allowed-chats`, `--allowed-users`.

WhatsApp Web pairs over QR:

```bash
sy channel whatsapp-web pair --agent assistant            # safe defaults: trigger !soulacy, no groups
sy channel whatsapp-web pair --agent assistant --trigger '!ask' --allow-groups
sy channel whatsapp-web status                            # connection state + QR payload
```

## Skills & registries

For a Git URL, the unified installer detects whether the repository contains a
Skill or MCP server, shows the safety/approval step, installs it persistently,
and registers MCP servers automatically:

```bash
sy package install https://github.com/owner/repository --allow-unverified

# Delegate cloning, scanning, building, registration, and activation to a remote gateway.
sy --gateway https://soul.example.com package install \
  https://github.com/owner/repository \
  --allow-unverified --allow-host-build
```

Remote package installation creates an asynchronous gateway job and reports
its progress until completion. `--allow-unverified` records approval of the raw
Git source; `--allow-host-build` separately records approval for the gateway to
build source and create package environments. Team and Scale deployments may
restrict host builds to their approved catalog even when both flags are set.

The built-in **System** agent uses this same installer when you say “Install
the Skill/MCP server from this URL.” Existing installations are reported and
left unchanged.

```bash
sy skill list
sy skill get pdf-tools
sy skill install ./my-skill                      # local directory
sy skill install self-improving-agent            # registry slug
sy skill install github.com/user/my-skill        # git source
sy skill install some-skill --yes                # skip consent prompt
```

Local installs resolve through the `registries:` config block (falling back to
a bare git provider), run the safety introspection pipeline (static scan +
sandboxed dry-run), show a consent prompt, then hot-load via the gateway's
`/skills/rescan` API. With a remote target, a registry slug or package URL is
delegated to the gateway; a local skill directory is refused rather than copied
or installed on the client by mistake.

!!! note "`--yes` never bypasses danger"
    `--yes` skips the routine consent prompt, but a **danger** safety
    verdict always requires an interactive yes.

Manage skill sources:

```bash
sy registry list                                  # configured sources
sy registry probe https://www.skills.sh/          # review what a URL is
sy registry add https://www.skills.sh/            # probe + consent + save
sy registry add https://reg.example.com --id main --priority 10 -y
```

`probe` runs client-side (no gateway needed). `add` saves through the gateway
API for a remote target. A remote API or network failure is returned to the
caller and never falls back to editing the client's `config.yaml`.

## MCP registration

```bash
# Remote HTTP/SSE-compatible endpoint. Repeat --header as needed.
sy --context personal mcp add --name company-crm --transport http \
  --url https://mcp.example.com/mcp \
  --header 'Authorization=Bearer ${CRM_TOKEN}'

# Personal edition can register a stdio process on the remote gateway host.
sy --context personal mcp add --name filesystem --transport stdio \
  --command /srv/soulacy/mcp-servers/filesystem/venv/bin/mcp-server-filesystem \
  --args '--root,/srv/soulacy/files' \
  --env 'LOG_LEVEL=info'
```

Remote registration uses an idempotent `PUT /api/v1/mcp/own/:id`; repeating the
same command updates that server. Arguments and environment variables apply to
stdio, while URL and headers apply to HTTP. Team and Scale policy rejects stdio
registrations and non-HTTPS remote endpoints. `mcp add --pip` is intentionally
local-only; use `package install --allow-host-build` for a remote source build.
Personal stdio registration does not search the host `PATH`: its executable
must already exist under that gateway workspace's managed `mcp-servers/`
directory. Symlinks are resolved before the path-boundary check.

## Memory, schedule & logs

```bash
sy memory list --agent support-bot     # session memory entries
sy schedule list                       # scheduled agent entries
sy logs --follow                       # stream live events
```

## Workspace

```bash
sy workspace info                  # resolved layout (soulspace vs legacy) + every path
sy workspace migrate --dry-run     # print the migration plan, move nothing
sy workspace migrate               # migrate legacy ~/.soulacy → soulspace (confirm; -y to skip)
```

Stop the gateway before `migrate` — databases move as files. See
[Workspace Layout](../configuration/workspace.md).

## Gateway control

```bash
sy server status     # GET /health against the gateway
sy server start      # convenience hint — run the `soulacy` binary for production
sy update check --manifest ./release-manifest.json
sy update install --manifest ./release-manifest.json --dry-run
sy update install --manifest ./release-manifest.json --yes
sy version           # CLI version + resolved gateway URL
```

When `updates.manifest_url` is configured in `config.yaml` or
`SOULACY_UPDATE_MANIFEST`, `sy update check` and `sy update install` use it by
default. `sy launch check` also reports whether that production upgrade path is
configured.

---

## The `soulacy` binary

Running `soulacy` with no subcommand starts the gateway in the
foreground, loading config from `SOULACY_CONFIG_PATH` or the
[workspace](../configuration/workspace.md):

```bash
soulacy                                          # start the gateway
SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml soulacy
```

### `soulacy build`

Build a flavored binary with extra driver modules compiled in
(see [Custom Distributions](../extend/custom-distributions.md)):

```bash
soulacy build --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
```

| Flag | Default | Description |
|------|---------|-------------|
| `--with` | — | Extra driver module, `module[@version]` (repeatable) |
| `-o` | `bin/soulacy` | Output binary path |
| `--skip-verify` | `false` | Skip conformance/registry test gates |
| `--keep` | `true` | Keep the generated `builtins_extra.go` (required for rebuilds) |

### `soulacy registry serve` / `keygen`

Host your own package registry
(see [Package Registries](../extend/registries.md)):

```bash
# Generate a signing keypair (private key written 0600; public key printed)
soulacy registry keygen --out ~/.soulacy/registry-signing.key

# Serve <slug>-<version>.tar.gz archives, signed
soulacy registry serve --dir ./packages --addr 127.0.0.1:18790 \
    --signing-key-file ~/.soulacy/registry-signing.key
```

Consumers put the printed **public** key in their `registries:` entry as
`signing_key` — unsigned or tampered packages are then refused.

| `serve` flag | Default | Description |
|--------------|---------|-------------|
| `--dir` | `packages` | Directory of `<slug>-<version>.tar.gz` archives |
| `--addr` | `127.0.0.1:18790` | Listen address |
| `--signing-key-file` | — | Hex ed25519 private key; when set, every package is signed |
