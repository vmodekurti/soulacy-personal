# Soulacy

<img src="docs/assets/living-core-blue-v1.png" width="64" height="64" alt="Soulacy Blue Living Core logo" />

**One binary. YAML agents. Runs anywhere — no cloud required.**

Soulacy is a **local-first agent operating system**. Write an agent in a single YAML file (or generate one from plain English in Studio), point it at any LLM (Ollama, OpenAI, Anthropic, Groq, or anything OpenAI-compatible), and run it from a terminal or a $5 VPS with no infrastructure setup, no Docker orchestration, and no cloud dependency.

You get more than a runtime: Studio for authoring and healing agents, Channels for delivering to Soulacy Mobile / Telegram / Slack / Discord / WhatsApp / email / Teams / Google Chat / HTTP, Schedule for cron and one-shot triggers, Learning for making the same mistake less often, and packaging for versioned installs — all in the same binary, all local by default.

Think of it as Ollama — but for agents.

## Start with a useful, checkable task

- [First successful run](https://docs.soulacy.io/getting-started/quickstart/):
  connect one model, summarize supplied notes, and test missing information.
- [Connect your iPhone](https://docs.soulacy.io/getting-started/iphone/): pairing,
  network checks, keyboard controls, permissions, and notification troubleshooting.
- [Six worked use cases](https://docs.soulacy.io/use-cases/): notes, handbook Q&A,
  a phone briefing, reviewed learning, Safe Undo, and a checked agent release.
- [Find the failing step](https://docs.soulacy.io/troubleshooting/first-checks/):
  distinguish networking, login, model, tool, delivery, and uncertain-write problems.

Guides track `main`; an installed gateway or iOS build may lag the source.
Safe Undo requires compatible configured resources, learning requires review,
and verification means the configured checks passed—not that every fact is true.

## Personal edition

Soulacy Commercial is the separate SaaS offering. This repository and its
installation instructions cover self-hosted Personal, not SaaS account setup.
See [the edition guide](https://docs.soulacy.io/editions/).

This public repository is the complete Soulacy Personal edition. It is
Apache-2.0 licensed, self-hosted, and has no dependency on the private Teams or
Scale codebases. Personal development, issues, releases, and source history
remain public here.

Contributions use the [Developer Certificate of Origin](DCO.md). Code is
Apache-2.0 licensed; the project name and logos follow the separate
[trademark policy](TRADEMARKS.md).

**Build it. Run it. Fix and learn.**

- **Build it** — describe the automation in plain English in Studio, or start from
  a vetted template. Soulacy drafts the plan, generates the workflow, and checks
  it end-to-end before you save.
- **Run it** — deploy the agent to Soulacy Mobile, Telegram, Slack, Discord, WhatsApp, HTTP, or a
  schedule. One binary, no cloud required.
- **Fix and learn** — when a run fails, Debug in Studio explains it in plain
  English and proposes a fix you can preview. Successful repairs become
  regression tests, and Soulacy shows you what it's learned over time.

[![CI](https://github.com/vmodekurti/soulacy-personal/actions/workflows/ci.yml/badge.svg)](https://github.com/vmodekurti/soulacy-personal/actions/workflows/ci.yml)
[![Release](https://github.com/vmodekurti/soulacy-personal/actions/workflows/release.yml/badge.svg)](https://github.com/vmodekurti/soulacy-personal/releases/latest)
[![Docker](https://img.shields.io/badge/ghcr.io-vmodekurti%2Fsoulacy--personal-blue?logo=docker)](https://github.com/vmodekurti/soulacy-personal/pkgs/container/soulacy)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

## Why Soulacy

| | Soulacy | n8n / Flowise / Dify | LangGraph / AutoGen |
|---|---|---|---|
| **Deploy** | Gateway with embedded UI; [requirements vary](docs/deployment/footprint.md) | Docker + Postgres + Redis | Python package |
| **Config** | One YAML file per agent | Visual editor (brittle exports) | Code |
| **Runs on** | Laptop, VPS, Raspberry Pi | Needs a server stack | Dev machine |
| **LLM** | Any — local or cloud | Mostly cloud | Any |
| **No-code** | GUI included in binary | Yes | No |

The field is crowded with frameworks that assume you want to write Python and deploy to the cloud. Soulacy is for people who want agents that just run — the same way you `ollama run llama3`.

---

### Install

### Cloud launchers

[Deploy to AWS](https://us-east-1.console.aws.amazon.com/cloudformation/home?region=us-east-1#/stacks/create/review?templateURL=https%3A%2F%2Fsoulacy-public-deploy-633654243571.s3.us-east-1.amazonaws.com%2Fcloudformation.yaml&stackName=SoulacyPersonal)
· [Deploy to Azure](https://portal.azure.com/#create/Microsoft.Template/uri/https%3A%2F%2Fraw.githubusercontent.com%2Fvmodekurti%2Fsoulacy-personal%2Fmain%2Fdeploy%2Fazure%2Fazuredeploy.json)
· [Deploy to Railway](https://railway.com/deploy/soulacy-personal?utm_medium=integration&utm_source=button&utm_campaign=soulacy-personal)
· [Deploy to Render](https://render.com/deploy?repo=https%3A%2F%2Fgithub.com%2Fvmodekurti%2Fsoulacy-personal)
· [Deploy with Coolify](docs/deployment/cloud.md#coolify)

AWS and Azure provision the production-shaped Postgres + Qdrant stack. Render,
Railway, and Coolify provide simpler single-container Personal deployments with
persistent workspace storage. See the [cloud deployment guide](docs/deployment/cloud.md)
for setup and security details.

Each cloud path ends the same way: open the HTTPS URL with the deployment's
login key, choose **Mobile → Pair a device**, and scan the short-lived QR code
from Soulacy for iOS. The permanent gateway key is never placed in the QR code.

### One line — macOS & Linux

```bash
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash
```

What it does, with zero questions asked:

1. Downloads the matching release tarball when one exists; otherwise builds from source (requires `git`, Go 1.26.6+, and `npm` for the GUI build).
2. Builds the Svelte GUI and compiles `soulacy` (the gateway) + `sy` (the CLI).
3. Installs both binaries into `~/.local/bin` (no `sudo`).
4. Prints clear next steps + offers to launch the gateway on the spot.

When you run `soulacy serve` (either right away or later), the gateway prints a one-time banner with the URL and a freshly-generated API key. Then open <http://127.0.0.1:18789>, paste the key, and you're in. The runtime workspace (`~/.soulacy/soulspace/`), config file, starter agent, and API key are all created automatically on first launch — you never have to touch a config file.

Overrides:

```bash
# install from a branch / tag / sha
SOULACY_REF=feature/integrated-roadmap curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/feature/integrated-roadmap/install.sh | bash

# install system-wide (uses sudo)
SOULACY_PREFIX=/usr/local curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash
```

### From a local checkout

```bash
git clone https://github.com/vmodekurti/soulacy-personal
cd soulacy
./install.sh                  # same behavior; will offer LaunchAgent setup on macOS
```

### Docker — guided deploy script (recommended)

From a checkout, [`scripts/docker-deploy.sh`](scripts/docker-deploy.sh) builds the image, runs
the container, publishes a host port, waits for the gateway to become healthy,
and prints the URL plus the real API key. Every parameter can be entered
interactively, passed as a flag, or set via an environment variable.

```bash
git clone https://github.com/vmodekurti/soulacy-personal
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

### Docker — embedded storage (no separate database service)

Build the image, then run it. Note two requirements: bind to `0.0.0.0` inside
the container (otherwise the published port can't reach the gateway), and choose
your host port via the left side of `-p`.

```bash
docker build -t soulacy .
docker run -d --name soulacy \
  -p 9000:18789 \
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
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/.env.example
cp .env.example .env   # set POSTGRES_PASSWORD, your LLM key, and SOULACY_PORT
docker compose up
```

The compose file publishes `${SOULACY_PORT:-18789}` on the host — set
`SOULACY_PORT` in `.env` to change it. Open
[http://localhost:18789](http://localhost:18789) (or your chosen port).

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
  port: 18789
  api_key: ""          # set to protect the gateway

llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3"
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
  provider: ollama
  model: llama3.3:70b

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
git clone https://github.com/vmodekurti/soulacy-personal
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
