# Installation

The fastest path is the one-command installer — it brings its own dependencies.

## One command (macOS / Linux — recommended)

```bash
curl -fsSL https://soulacy.io/install.sh | bash
```

What it does:

1. Detects your OS/architecture (macOS & Linux, amd64 & arm64).
2. Downloads the latest release binaries — or, if no release is published yet,
   **builds from source automatically**, fetching private copies of Go and
   Node into `~/.soulacy/toolchain` when they're missing (no Homebrew, no
   system package changes).
3. Installs `soulacy` (the gateway) and `sy` (the CLI) to `/usr/local/bin`.
4. Creates your workspace at `~/.soulacy/soulspace` with a default config.
5. Offers to install [Ollama](https://ollama.com) and pull `llama3` so you
   have a local LLM out of the box.
6. Offers to start the gateway and open the GUI at `http://localhost:1947`.

!!! tip "Pin a version"
    `SOULACY_VERSION=v0.1.11 curl -fsSL https://soulacy.io/install.sh | bash`

## Requirements

| | |
|---|---|
| OS | macOS 13+ or Linux (amd64 / arm64) |
| Tools | `curl` and `tar` — everything else is installed automatically |
| LLM | Ollama (local, free) **or** an OpenAI / Anthropic / Gemini API key |

## Build from source

```bash
git clone https://github.com/vmodekurti/soulacy.git
cd soulacy
make all          # GUI + gateway + CLI → ./bin/soulacy and ./bin/sy
sudo install -m755 bin/soulacy bin/sy /usr/local/bin/
```

`make all` needs Go 1.26.6+ and Node 18+ on your PATH (`make build` alone skips
the GUI — the binary embeds the web UI at compile time, so use `make all`).

## Docker

The Docker quick start is a **Personal mode** deployment. Team and Scale need a
separate isolated execution-worker plane; adding PostgreSQL to Compose alone
does not make a deployment multi-user safe. Never mount `docker.sock` into the
Soulacy gateway. See [Docker deployment](../deployment/docker.md#team-and-scale)
and [Deployment modes](../configuration/deployment-modes.md).

From a checkout (works today, builds the image locally):

```bash
git clone https://github.com/vmodekurti/soulacy.git && cd soulacy
docker compose up --build -d
```

The gateway listens on **1947**; state persists in the
`/home/soulacy/.soulacy` volume:

```bash
docker run -d --name soulacy \
  -p 1947:1947 \
  -v soulacy-data:/home/soulacy/.soulacy \
  ghcr.io/vmodekurti/soulacy:latest   # published with tagged releases
```

More (Compose details, reverse proxies): [Docker deployment guide](../deployment/docker.md).

## AWS Team Lite pilot

For a small multi-user evaluation, the repository includes a Team Lite AWS
profile targeted at less than `$200` of low-traffic infrastructure for one
month. It remains Soulacy Team mode with PostgreSQL, authenticated workspaces,
KMS credentials, mTLS NATS, and a separate signed/gVisor worker. It reduces
capacity and availability, not tenant security controls.

For a Cloudflare-hosted domain with Google login, the guided macOS wizard
creates the delegated DNS zone, writes the non-secret Terraform configuration,
and launches Team Lite:

```bash
deploy/aws/team-lite-quickstart.sh
```

It opens the Cloudflare and Google setup pages and prints the exact token
permissions, origin, and callback URL. Provider secrets are never written to
the Terraform variables file.

After preparing `deploy/aws/terraform.tfvars` as described in the
[automated AWS guide](https://github.com/vmodekurti/soulacy/blob/main/deploy/aws/README.md), run:

```bash
SOULACY_AWS_BUDGET_EMAIL=you@example.com deploy/aws/deploy.sh --mode team-lite
```

The profile creates a `$180` account-wide AWS Budget. A budget is an alert, not
a hard spending stop; review Cost Explorer daily and destroy the pilot after
the evaluation.

## Pre-built binaries

Tagged releases publish `soulacy_<version>_<os>_<arch>.tar.gz` bundles
(each contains both `soulacy` and `sy`) on
[GitHub Releases](https://github.com/vmodekurti/soulacy/releases):

```bash
grep 'soulacy_v0.1.11_darwin_arm64.tar.gz' checksums.sha256 | shasum -a 256 -c -
tar -xzf soulacy_v0.1.11_darwin_arm64.tar.gz
sudo install -m755 soulacy sy /usr/local/bin/
```

Releases also include `release-manifest.json`, which records the release
version, source commit, generation time, and every artifact's OS, architecture,
byte size, and SHA-256 digest for installer and CI checks.

If the releases page is empty, use the one-command installer above — it
falls back to a source build automatically.

## Verify

```bash
soulacy --version  # gateway version
sy version         # CLI + framework version
sy doctor          # checks workspace, config, providers, and the gateway
```

Both `soulacy --version` and `sy version` are supported in v0.1.8. If a build
prints only a commit hash, it was probably installed from source; use a tagged
release when you want `sy update` to compare versions automatically.

## Understand where configuration lives

Fresh installations use:

```text
~/.soulacy/soulspace/config.yaml
```

Soulacy resolves configuration in this order:

1. `SOULACY_CONFIG_PATH` when explicitly set;
2. `config.yaml` inside `SOULACY_WORKSPACE` or the resolved workspace;
3. the legacy `~/.soulacy/config.yaml` location;
4. `./config.yaml` for a development checkout.

Services do not necessarily share your login user's home or environment. If a
foreground gateway accepts an API key but systemd does not, follow the
[Linux/VPS service configuration checklist](../deployment/linux.md#5-prove-the-service-loaded-the-intended-config).

## What's next?

1. `sy onboard` — the guided first-run path for provider, search, starter agent, update manifest, and auto-start.
2. Follow the [Quick Start](quickstart.md), then take the [GUI tour](gui-tour.md).
