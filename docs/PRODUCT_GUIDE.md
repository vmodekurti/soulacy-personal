# Soulacy Product Guide

Soulacy is a local-first agent operating system: you describe an automation, it builds a declarative agent you can inspect, then runs it on schedules and channels with visible repair, learning, and governance. This guide takes you from install to a useful assistant in about ten minutes, then points to the deeper docs when you need them.

## What Soulacy is

A single Go binary (`soulacy`) runs a gateway with an embedded web GUI; a companion CLI (`sy`) handles setup, diagnostics, and daemon management. Agents are plain `SOUL.yaml` files you can read, diff, and version. Everything runs on your own machine or server — providers, secrets, memory, and schedules are yours.

Three promises, repeated everywhere in the product:

1. **Build it** — describe the automation and let Studio create the agent or workflow, then inspect or edit it.
2. **Run it** — trigger from chat, a channel, cron, a webhook, or another agent.
3. **Fix and learn** — when something fails, Studio diagnoses the real run, repairs the workflow, and turns successful patterns into reviewed learnings.

## Install

The fastest path builds and installs both binaries locally:

```
make all        # build the GUI and the soulacy/sy binaries
make install    # copy them onto your PATH (defaults to ~/.local/bin)
sy setup        # interactive first-run: pick a provider, set a key, choose defaults
```

To keep Soulacy running in the background, register it as an OS service:

```
make service-install    # systemd (Linux) or launchd (macOS)
```

Upgrades are non-destructive and reversible:

```
make upgrade     # rebuild + install, backing up the prior build as *.prev
make rollback    # restore the previous build if an upgrade misbehaves
sy update check --manifest ./release-manifest.json
sy update install --manifest ./release-manifest.json --dry-run
sy update install --manifest ./release-manifest.json --yes
```

For production workspaces, set `updates.manifest_url` in `config.yaml`
or `SOULACY_UPDATE_MANIFEST`. Dashboard readiness and `sy launch check`
will then show that the runtime has a verified upgrade path, and `sy update
check/install` can run without repeating `--manifest`.

## Your first agent

Open the GUI (the gateway prints its URL on start) and go to **Templates**. Each template shows a setup checklist — required API keys, schedule, and delivery channel — derived automatically from the agent definition. Good first picks:

- **Web researcher** — answers with cited sources.
- **Daily check-in assistant** — a friendly morning briefing on a schedule.
- **Daily stock screener / Flight deal finder** — scheduled, sourced, channel-delivered.
- **GitHub issue triage / Research brief generator** — on-demand structured output.

Install a template, fill the checklist, and use **Try** to run it once with a test prompt before enabling it for real.

To build something bespoke, open **Studio**, describe the automation in plain language, and let it generate a plan you can edit. Advanced users can add Python, tool, and agent blocks directly.

## Running agents on channels and schedules

Agents trigger from chat, cron, an inbound webhook, or another agent. Configure delivery on the **Schedule** and **Channels** pages: a cron agent can post its result to Telegram, Slack, Discord, WhatsApp, or a generic outgoing webhook. Set a default outbound bot once and non-interactive agents just work. `notify_on_failure` sends a heads-up when a scheduled run errors.

## Safety and governance

Soulacy is meant to be trusted with real work, so high-risk actions are gated.

- **Tool policy.** Add a `policy:` block to an agent (or use the Agents editor's “+ policy” helper) to gate shell, file, and network tools with `allow` / `prompt` / `deny`, plus domain allow/deny lists and path deny-globs. Denied actions never reach a handler.
- **Dry run.** Set `dry_run: true` on an agent, or send the `X-Soulacy-Dry-Run: true` header, to simulate side-effecting tools (shell, file writes, network POSTs, MCP/plugin calls) without executing them. Read-only tools still run so the agent can reason.
- **Approvals anywhere.** When a tool needs confirmation, the request appears in the GUI and on the **Mobile** page of any paired device. Approve or deny from your phone; enable push notifications to be alerted.
- **Doctor.** `sy doctor` (and the GUI Providers page) checks provider reachability and verifies the encrypted vault actually decrypts, flagging any provider missing a key.

## Where agents run

By default tool code runs locally in a sandboxed subprocess. Per agent you can select a different backend with an `execution.backend` value:

- `local` — sandboxed subprocess (default).
- `docker` — a short-lived container; mounts are limited to an explicit volume allowlist.
- `ssh` — a remote host; the private key can be pulled from the encrypted vault instead of a file.
- `modal` / `runpod` / `daytona` — cloud sandbox presets (the provider CLI must be installed and authenticated on the host).

## Learning that you can see

Enable `learning.enabled` on an agent and successful runs produce reviewable proposals — memories, procedures, and installable skills — that you accept or reject in **Brain Memory**. The “Is it working?” panel there shows longitudinal evidence: how often accepted skills get reused and whether recurring errors decline after learning is switched on.

## The mobile companion

The GUI is an installable PWA. On the **Mobile** page you can pair a phone (generate a code on one device, enter it on the other), enable push notifications, use Pocket Chat with any chat-ready agent, trigger scheduled jobs, test channel delivery, and review or resolve pending tool approvals. Pairing issues a scoped credential so the phone can reach the gateway.

The companion page also works as a small operations console: active and recent runs link directly to Activity, scheduled agents can be started or have their output path tested, and Delivery Health shows each configured channel or bot mapping with a dry check and a live send-test action.

## Observability and repair

The **Dashboard** streams a live event log and surfaces proactive suggestions (schedule a repeated manual task, review a flaky agent, enable learning). **Activity** shows every run; a failed run can be sent to **Studio**, which diagnoses it from the action log and proposes a verified fix. The **Browser** page replays an agent's web automation step by step with action filters, screenshot gallery, shareable deep links, and JSON trace export.

## Troubleshooting

- Something misconfigured? Run `make health` (or `sy doctor`) for provider, vault, and channel diagnostics.
- Filing an issue? `make logs-bundle` (or `sy support bundle`) collects a redacted support bundle with doctor output, release/update metadata, masked config, masked agent manifests, and recent log tails. The Dashboard/Config download also includes live launch readiness from the running gateway.
- After an upgrade, `make which` confirms which binary your shell actually runs.

## Going deeper

- Agent model and `SOUL.yaml`: `docs/agents/soul-yaml.md`, `docs/FRAMEWORK_OVERVIEW.md`
- Studio and Python tools: `docs/STUDIO_REDESIGN.md`, `docs/STUDIO_PYTHON_TOOLS.md`
- Channels and scheduled output: `docs/channels/`, `docs/using/schedules.md`
- Extensibility (plugins, external channels/storage): `docs/EXTENSIBILITY.md`, `docs/PLUGIN_MANIFEST.md`
- Competitive positioning and roadmap: `docs/SOULACY_FUNCTIONALITY_COMPETITIVE_REPORT.md`
