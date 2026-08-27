# Soulacy

**One binary. YAML agents. Runs anywhere — no cloud required.**

Soulacy is a **local-first agent operating system**. Write an agent in a single YAML file (or generate one from plain English in Studio), point it at any LLM (Ollama, OpenAI, Anthropic, Groq, or anything OpenAI-compatible), and run it from a terminal or a $5 VPS with no infrastructure setup, no Docker orchestration, and no cloud dependency.

You get more than a runtime: Studio for authoring and healing agents, Channels for delivering to Telegram / Slack / Discord / WhatsApp / email / Teams / Google Chat / HTTP, Schedule for cron and one-shot triggers, Learning for making the same mistake less often, and packaging for versioned installs — all in the same binary, all local by default.

Think of it as Ollama — but for agents.

[Website](https://soulacy.io) · [Documentation](https://docs.soulacy.io)

### Team/Scale administrator sign-in

Open `https://your-soulacy-host/admin/setup` once on a new deployment. Enter
the `server.api_key` held by the host operator and create the initial tenant
catalog. For normal platform operations, use `/admin`: the deployment
administrator can create organizations and workspaces and designate each
workspace's first owner without becoming a workspace member. The same control
plane can suspend and reactivate organizations or individual workspaces with
an audited reason and explicit confirmation.

The initial bootstrap is atomic and can run only once. Each new workspace gets
a one-time setup link for its designated owner. That owner selects and validates
the workspace's OIDC provider; the provider and issuer are then locked. Google
Workspace, Microsoft Entra ID, Okta, Auth0, Keycloak, and standards-based OIDC
issuers are supported. GitHub OAuth and Sign in with Apple require
provider-specific flows and are not advertised as generic OIDC.

The deployment key does not become a hidden workspace administrator: it remains
limited to deployment-wide operations. See [Platform administration](https://docs.soulacy.io/configuration/platform-administration/)
and [Workspace identity and login](https://docs.soulacy.io/configuration/workspace-identity/).

**Build it. Run it. Fix and learn.**

- **Build it** — describe the automation in plain English in Studio, or start from
  a vetted template. Soulacy drafts the plan, generates the workflow, and checks
  it end-to-end before you save.
- **Run it** — deploy the agent to Telegram, Slack, Discord, WhatsApp, HTTP, or a
  schedule. One binary, no cloud required.
- **Fix and learn** — when a run fails, Debug in Studio explains it in plain
  English and proposes a fix you can preview. Successful repairs become
  regression tests, and Soulacy shows you what it's learned over time.

[![CI](https://github.com/vmodekurti/soulacy/actions/workflows/ci.yml/badge.svg)](https://github.com/vmodekurti/soulacy/actions/workflows/ci.yml)
[![Release](https://github.com/vmodekurti/soulacy/actions/workflows/release.yml/badge.svg)](https://github.com/vmodekurti/soulacy/releases/latest)
[![Docker](https://img.shields.io/badge/ghcr.io-vmodekurti%2Fsoulacy-blue?logo=docker)](https://github.com/vmodekurti/soulacy/pkgs/container/soulacy)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

## Why Soulacy

| | Soulacy | n8n / Flowise / Dify | LangGraph / AutoGen |
|---|---|---|---|
| **Deploy** | Single binary, zero deps | Docker + Postgres + Redis | Python package |
| **Config** | One YAML file per agent | Visual editor (brittle exports) | Code |
| **Runs on** | Laptop, VPS, Raspberry Pi | Needs a server stack | Dev machine |
| **LLM** | Any — local or cloud | Mostly cloud | Any |
| **No-code** | GUI included in binary | Yes | No |

The field is crowded with frameworks that assume you want to write Python and deploy to the cloud. Soulacy is for people who want agents that just run — the same way you `ollama run llama3`.

---

### Install

### One line — macOS & Linux

```bash
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/main/install.sh | bash
```

What it does, with zero questions asked:

1. Downloads the matching release tarball when one exists; otherwise builds from source (requires `git`, Go 1.26.6+, and `npm` for the GUI build).
2. Builds the Svelte GUI and compiles `soulacy` (the gateway) + `sy` (the CLI).
3. Installs both binaries into `~/.local/bin` (no `sudo`).
4. Prints clear next steps + offers to launch the gateway on the spot.

When you run `soulacy serve` (either right away or later), the gateway prints a one-time banner with the URL and a freshly-generated API key. Then open <http://127.0.0.1:1947>, paste the key, and you're in. The runtime workspace (`~/.soulacy/soulspace/`), config file, starter agent, and API key are all created automatically on first launch — you never have to touch a config file.

Overrides:

```bash
# install from a branch / tag / sha
SOULACY_REF=feature/integrated-roadmap curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/feature/integrated-roadmap/install.sh | bash

# install system-wide (uses sudo)
SOULACY_PREFIX=/usr/local curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/main/install.sh | bash
```

### From a local checkout

```bash
git clone https://github.com/vmodekurti/soulacy
cd soulacy
./install.sh                  # same behavior; will offer LaunchAgent setup on macOS
```

### Docker — guided deploy script (recommended)

> **Deployment boundary:** the Docker commands in this section are Personal
> mode deployments. They intentionally do not mount `/var/run/docker.sock`.
> Team and Scale require an independently operated, signed-image execution
> worker and must not give the gateway control of a container daemon. Use the
> [AWS deployment](deploy/aws/README.md) or follow the
> [production execution-plane guide](docs/configuration/production-runtime.md).
> For a small one-month AWS evaluation, the same deployment includes
> `--mode team-lite`: full Team application boundaries on cost-bounded,
> reduced-availability infrastructure with an AWS Budget guardrail.
> Cloudflare DNS plus Google login can be bootstrapped interactively with
> `deploy/aws/team-lite-quickstart.sh`.

From a checkout, [`scripts/docker-deploy.sh`](scripts/docker-deploy.sh) builds the image, runs
the container, publishes a host port, waits for the gateway to become healthy,
and prints the URL plus the real API key. Every parameter can be entered
interactively, passed as a flag, or set via an environment variable.

```bash
git clone https://github.com/vmodekurti/soulacy
cd soulacy
./scripts/docker-deploy.sh                       # interactive — prompts for each setting
./scripts/docker-deploy.sh --yes                 # accept defaults, no prompts
./scripts/docker-deploy.sh --host-port 9000      # publish on a different host port
```

When it finishes it prints a summary like:

```
  Deployed:   soulacy
  URL:        http://localhost:9000
  API key:    sy_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  Logs:       docker logs -f soulacy
  Shell:      docker exec -it soulacy bash
  Stop:       docker rm -f soulacy
```

Useful flags: `--host-port`, `--container-port`, `--name`, `--data-dir`,
`--api-key`, `--no-build`, `--yes`. Run `./scripts/docker-deploy.sh --help` for the
full list.

### Docker — lightweight (SQLite, zero dependencies)

Build the image, then run it. Note two requirements: bind to `0.0.0.0` inside
the container (otherwise the published port can't reach the gateway), and choose
your host port via the left side of `-p`.

```bash
docker build -t soulacy .
docker run -d --name soulacy \
  -p 9000:1947 \
  -e SOULACY_SERVER_HOST=0.0.0.0 \
  -e SOULACY_LLM_PROVIDERS_OLLAMA_BASE_URL=http://host.docker.internal:11434 \
  -v ~/.soulacy:/home/soulacy/.soulacy \
  soulacy
```

> **Reaching a host-side Ollama:** inside the container `localhost` is the
> container itself, so a local Ollama on your machine is *not* at
> `localhost:11434` from the gateway's view. Point it at the host with
> `SOULACY_LLM_PROVIDERS_OLLAMA_BASE_URL=http://host.docker.internal:11434`
> (shown above). On Linux also add `--add-host host.docker.internal:host-gateway`.
> Cloud LLM providers (OpenAI, Anthropic, etc.) need none of this — outbound
> internet works by default. `scripts/docker-deploy.sh` handles all of this via
> its `--ollama-host` flag (defaulting to `host.docker.internal:11434`).

Open [http://localhost:9000](http://localhost:9000). The gateway generates an API
key on first run and stores it in the mounted config; read it back with:

```bash
docker exec soulacy sh -c 'grep api_key ~/.soulacy/config.yaml'
```

### Docker — full stack (Postgres + Qdrant + GUI)

```bash
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy/main/.env.example
cp .env.example .env   # set POSTGRES_PASSWORD, your LLM key, and SOULACY_PORT
docker compose up
```

The compose file publishes `${SOULACY_PORT:-1947}` on the host — set
`SOULACY_PORT` in `.env` to change it. Open
[http://localhost:1947](http://localhost:1947) (or your chosen port).
PostgreSQL in this Compose file improves durability; it does not turn the
deployment into Team mode. The file pins `deployment.mode=personal` explicitly.

### Team and Scale deployment

Team and Scale split the trusted control plane from untrusted execution:

```text
gateway containers → TLS-authenticated durable queue → dedicated OCI workers
```

Workers use a digest-pinned, signature-verified image and a hardened runtime
such as gVisor. The gateway never receives `docker.sock`, and workload
containers never receive the worker's runtime socket. `/ready` verifies
PostgreSQL, the durable queue, and a live worker round trip before admitting
traffic. See [Deployment modes](docs/configuration/deployment-modes.md),
[Docker deployment](docs/deployment/docker.md), and
[Automated AWS deployment](deploy/aws/README.md).

### Running CLI commands against a container

The image bundles the `sy` CLI. There's no SSH — use `docker exec`:

```bash
docker exec -it soulacy bash        # interactive shell inside the container
docker exec -it soulacy sy status   # run a single CLI command
```

Inside the container `sy` auto-discovers the gateway and its API key from the
mounted config, so no flags are needed.

### Python SDK (experimental)

> **Experimental, not yet published to PyPI.** The Python SDK lives in
> [`sdk/python`](sdk/python) and currently has no test coverage. Install it
> from source until a release is published:
>
> ```bash
> pip install ./sdk/python
> ```

---

## Quick start

```bash
# List loaded agents
sy agent list

# Chat with an agent
sy chat --agent system "Hello!"

# Check gateway status
sy server status

# Validate an agent definition, including provider/model fit
sy agent validate examples/agents/hello-world/SOUL.yaml

# Run local diagnostics
sy doctor

# Stream live event log
sy logs --follow
```

---

## Configuration

Default config is created at `~/.soulacy/config.yaml` on first run.

```yaml
server:
  host: "127.0.0.1"
  port: 1947
  api_key: ""          # set to protect the gateway

llm:
  default_provider: nvidia
  providers:
    nvidia:
      base_url: "https://integrate.api.nvidia.com/v1"
      api_key: "" # Prefer SOULACY_LLM_PROVIDERS_NVIDIA_API_KEY
      model: "meta/llama-3.3-70b-instruct"
```

All fields can be overridden with environment variables: `SOULACY_<SECTION>_<KEY>`.  
Full reference: [docs/configuration/index.md](docs/configuration/index.md)

---

## What an agent looks like

```yaml
# ~/.soulacy/agents/daily-briefing/SOUL.yaml
id: daily-briefing
name: Daily Briefing
trigger: cron
schedule:
  cron: "0 7 * * *"   # 7 AM every day
  output:
    channel: "telegram-daily-briefing"
    to: "123456789"
    bot_name: "Daily Briefing Bot"

llm:
  # Omit provider/model to inherit the workspace defaults, or pin them here.
  provider: nvidia
  model: meta/llama-3.3-70b-instruct

tools:
  - name: get_weather
    python_file: tools/get_weather.py
    parameters:
      type: object
      properties:
        location: { type: string }
      required: [location]

system_prompt: |
  Every morning, fetch the weather for Chicago and send a
  2-sentence briefing to Telegram.
```

That's it. No boilerplate, no decorators, no SDK imports. Drop the file in, and Soulacy picks it up without a restart.

---

## Architecture

```
┌─────────────────────────────────────────────┐
│                  Gateway                     │
│  REST API  ·  WebSocket  ·  Embedded GUI     │
└──────────┬──────────────────────┬────────────┘
           │                      │
    ┌──────▼──────┐        ┌──────▼──────┐
    │   Runtime   │        │   Channels   │
    │  (engine)   │        │  Slack/TG/   │
    └──────┬──────┘        │  Discord/HTTP│
           │               └─────────────┘
    ┌──────▼──────────────────────────────┐
    │              Storage                │
    │  SQLite (default)  ·  Postgres      │
    │  sqlite-vec        ·  Qdrant        │
    └─────────────────────────────────────┘
```

---

## Development

```bash
git clone https://github.com/vmodekurti/soulacy
cd soulacy
make all          # builds GUI + Go binaries
make install      # installs to /usr/local/bin
make test         # runs Go tests
make docker-up    # starts full stack in Docker
```

See [docs/FRAMEWORK_OVERVIEW.md](docs/FRAMEWORK_OVERVIEW.md) for architecture details.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
