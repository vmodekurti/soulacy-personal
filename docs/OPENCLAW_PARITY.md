# Soulacy vs OpenClaw — Current Parity Snapshot

**Date:** 2026-07-14
**Posture:** Soulacy is now in launch-hardening mode. The remaining work is proof, polish, and operator confidence rather than new core primitives.

## TL;DR

Soulacy has closed the original load-bearing parity gaps from the June analysis:

- Guided setup exists through `sy onboard` and the GUI First Run page.
- Health checks exist through `sy doctor`, the Provider/Secrets Doctor API, and GUI provider/channel checks.
- Daemon management exists through `sy daemon`.
- Soulacy can act as an MCP server through `sy mcp serve`.
- Browser automation can be mounted through MCP with a headless Playwright quick-start, policy posture, trace replay, and authenticated local screenshot artifacts.
- Queues, Knowledge, Workboard, schedules, and agents are exposed through platform tools.
- Studio now has validation, autowiring, runtime diagnosis, and build-until-it-works repair loops.
- Verified update checks and installs exist through `sy update check/install`, release workflows sign artifacts, generate an SBOM, optionally codesign/notarize macOS binaries when secrets are present, and launch readiness reports whether a production update manifest is configured.

The remaining parity work is not "add more primitives." It is productization: live credential-backed regression, recurring screenshot refresh, and broader channel/community polish.

## Updated Parity Matrix

| Area | OpenClaw | Soulacy | Status |
|---|---|---|---|
| Install | Node/npm based install | Go binary with GUI embed; `make install` installs `soulacy` and `sy`; release packaging emits combined tarballs, checksums, manifest, signed artifacts, SBOM, Homebrew tap bump, `sy update check`, and verified `sy update install` | Strong once release binaries are published |
| Onboarding | `openclaw onboard` | `sy onboard` plus GUI First Run | Closed |
| Daemon | CLI-managed daemon | `sy daemon` for service lifecycle | Closed |
| Doctor | Broad doctor checks | `sy doctor`, GUI/API provider and channel doctor, support bundle, live browser/mobile/chat readiness artifacts, mobile delivery checks | Mostly closed; keep expanding from real failures |
| Channels | Very broad channel list | HTTP, Telegram, Slack, Discord, Email/SMTP, Teams, Google Chat, WhatsApp/WhatsApp Web, plus webhook/MCP extension path | Strong MVP coverage; keep adding adapters from real demand |
| MCP client | External MCP tools | External MCP tools | Closed |
| MCP server | Exposes OpenClaw to MCP clients | `sy mcp serve` exposes agents, chat, schedules, Workboard, KB, queues | Closed |
| Browser automation | Native/plugin browser control | Playwright MCP sidecar, headless template, process cleanup, per-agent domain policy docs, Browser trace page/API, trace export, screenshot gallery, and authenticated local screenshot serving | Closed for MVP |
| Multi-agent orchestration | Agent handoffs / teams | Peer agents, router agents, auto-delegate, transitive safety tiers, and opt-in parallel peer fan-out with deterministic result ordering | Strong for MVP |
| Studio/canvas | Assistant canvas/workflow surfaces | Studio workflow canvas, ReAct/Plan-Execute authoring, self-heal, run traces | Stronger for auditable workflows |
| Memory/learning | Persistent memory and skill learning | Episodic/semantic/procedural memory, proposals, accepted skill injection | Strong, still needs polished narrative |
| Queues | Plugin/storage primitives | Built-in ephemeral queue tools and GUI | Closed |
| Mobile companion | Native apps | Responsive Mobile operations page with Pocket Chat, approvals, active runs, retained run history, schedule actions, delivery checks, PWA install signals, and run-review readiness | MVP closed; native app remains deferred |
| Voice | Voice/wake/talk-back | Chat push-to-talk voice MVP with ephemeral OpenAI Realtime keys, readiness/parity visibility, and safe key handling | **Not v1 scope** (`docs/LAUNCH_STRATEGY.md` §9 decision 3). The Chat push-to-talk MVP is present but deliberately not marketed for v1.0.0 — the security wedge is the launch narrative and voice dilutes it. Re-scope post-launch if voice becomes a differentiator worth investing in. |
| Auto-update | npm/Sparkle-style update story | Manifest-backed `sy update check/install`, checksum verification, dry-run, backups, rollback docs, readiness/support-bundle visibility, signed release artifacts, SBOM, and optional macOS codesign/notarization | MVP closed |

## Soulacy Advantages To Preserve

- **Local-first, inspectable runtime:** SOUL.yaml stays on disk and is easy to diff, review, copy, or repair.
- **Single binary posture:** The production runtime should not require Node after build.
- **Studio as one-stop agent development:** Studio owns generation, validation, testing, debugging, and repair.
- **Safer operating model:** Capability tiers, tool confirmations, RBAC, vault-backed secrets, and doctor checks are first-class.
- **MCP as the extension layer:** Instead of copying every adapter, Soulacy can expose itself as MCP and mount outside MCP servers.
- **Unified memory model:** Episodic, semantic, and procedural memory are part of one runtime story.

## Remaining Launch Gaps

1. **Release packaging:** Release archives, install smoke tests, checksums, manifest, update check, verified `sy update install`, launch-readiness update-manifest checks, support-bundle release metadata, upgrade docs, rollback docs, Sigstore signing, SBOM, Homebrew tap update, and macOS codesign/notarization hooks now exist. Remaining work is running the release workflow with production secrets and publishing the first production tag.
2. **Regression suite:** Clean-runtime UAT now covers launch readiness, GUI/PWA, golden template presence/instantiation, queues, schedule, support bundles, update checks, Browser trace wiring, and opt-in live-model Studio build/repair traces. `sy eval` now supports reusable golden suites, tag filtering, repeat-based benchmark runs, fail-fast, secret-aware skips, latency p50/p95, token summaries, tool assertions, and channel-delivery assertions. Opt-in golden smokes now prove real Slack/Telegram/Discord delivery and Playwright MCP browser sidecar startup/tool discovery when credentials or local sidecar dependencies are available.
3. **Mobile/PWA companion:** Core phone-sized operations are now usable: Pocket Chat for enabled interactive agents, approvals, active run links, recent run history, schedule run/test actions, delivery health checks, PWA install signals, and run-review readiness. Remaining work is native app packaging if native presence becomes a launch requirement.
4. **Channel polish:** Default outbound routing, agent-specific bot mappings, dry diagnosis, live delivery tests, companion-surface checks, Teams/Google Chat/Email outbound adapters, and opt-in credential-backed golden delivery smoke exist. Remaining work is collecting more real-world failure signatures into the doctor and keeping setup docs current as adapters are added.
5. **Public docs:** Quickstarts, production deployment guide, channel setup guides, troubleshooting playbooks, strict docs build, GitHub Pages deploy workflow, canonical published installer, launch-readiness docs checks, and repeatable GUI screenshot evidence now exist. Remaining work is refreshing screenshots before launch tags.
6. **Browser control hardening:** Domain policies, Browser trace wiring, action filters, trace export, screenshot gallery, deep links, authenticated local screenshot artifacts, and Activity run-history browser trace handoffs exist. Remaining work is opt-in live browser smoke coverage in release runs.
7. **Voice/product narrative:** Not v1 scope (per `docs/LAUNCH_STRATEGY.md` §9 decision 3). Chat push-to-talk MVP remains in the code; re-scope after launch signal warrants investment.

## Current Recommendation

Do not add new architectural primitives unless they directly improve reliability or launch polish. The platform now has enough core machinery; the next competitive leap is running credential-backed UAT repeatedly, keeping docs screenshots fresh, and making every failure explainable and recoverable from Studio or Activity.
