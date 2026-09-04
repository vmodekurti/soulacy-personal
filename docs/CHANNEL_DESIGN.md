# Channel design — System agent exposure & channel↔agent connectivity

Design memo. No code changes here. Two questions, each grounded in the current implementation.

## Reference: what the code does today

Before recommending anything, this is what's actually wired:

**Each channel adapter holds one `agentID` field, set at construction.** Every adapter follows the same pattern:

- Telegram (`internal/channels/telegram/adapter.go:38-94`): `id` + `agentID` + optional `allowedUserIDs`. Inbox messages get `AgentID: a.agentID` stamped at line 151.
- Slack (`internal/channels/slack/adapter.go:175`): same — `AgentID: a.agentID`.
- Discord (`internal/channels/discord/adapter.go:198`): same.
- WhatsApp (`internal/channels/whatsapp/adapter.go:243`): same.
- HTTP (`internal/channels/http/adapter.go:60-82`): the outlier — `Receive(agentID, userID, username, text)` takes the agent ID *per request*, so HTTP is N:N at the message level. The web GUI's session-scoped agent selector and the public `POST /api/v1/chat` endpoint both end up here.

**Multiple bots per platform are supported on chat channels** via a `bots: [{token, agent_id, …}, …]` config block (`internal/app/wire.go:761-794` for Telegram, similar blocks at `:813-`, `:865-` for Discord and Slack). Each entry constructs a separate adapter with a unique adapter ID (`telegram` for the primary, `telegram-<agentID>` for extras). So at the platform layer, "Telegram" already supports N bots, but each individual bot is still 1:1 to an agent — by Telegram's design, one bot token = one bot identity.

**WhatsApp truly is 1:1 at the platform layer.** A WhatsApp Business number doesn't have a "bot" abstraction — it's a phone number that one webhook endpoint owns. We register one adapter per number, that adapter has one `agentID`. There's no escape from this in the WhatsApp API; the only way to route N agents through one number is to make the bound endpoint itself a router.

**`agent.Definition.Channels` is descriptive, not runtime-enforced.** The only consumer of this field at runtime is `internal/agentvalidate/validate.go:107`, which checks that a `trigger: channel` agent declares at least one channel at startup. The engine's `Handle` path never consults `def.Channels` when dispatching an inbound message — it just trusts `msg.AgentID`, which was stamped by the adapter at construction time. So the agent's "channels:" list is a hint to the operator about what they should wire up, not a guard.

**The System agent exists and is locked to web today.** `examples/agents/system/SOUL.yaml` is the all-in-one assistant — `skills: ['*']`, `agents: [brain-router, research-agent, financial-agent, decision-agent, strategy-agent, writing-agent, web-researcher, system-admin]`, `builtins: ['*']`, `mcp_servers: ['*']`, `mcp_tools: ['*']`, `system_tools: true`, `memory.read_scopes: [agent, session, global]`, 30 turns, 30-minute timeout, `reasoning.strategy: plan_execute`. Its `channels:` list is `[http]`.

The lockdown is enforced by an explicit guard in `internal/app/channels.go:101-110`:

```go
func externalChannelAgentAllowed(adapterID, agentID string, log *zap.Logger) bool {
    if strings.TrimSpace(agentID) != runtime.SystemAgentID {
        return true
    }
    log.Warn("external channel mapping skipped: system agent is web-only", …)
    return false
}
```

If a config tries to bind the System agent to Telegram/Discord/Slack/WhatsApp, the adapter is silently dropped at startup with a warn log. The single-bot legacy path and the multi-bot `bots:` lists both call this guard.

**`system_tools: true` is also gated at config level** (`pkg/agent/types.go:275-279`): "ALSO requires runtime.allow_system_tools: true in config.yaml — both must" be present. So even the agent's own opt-in to shell_exec requires a process-level second toggle.

---

## Q1 — System agent on all channels with a caution prompt?

**Short answer: no — not as the default. The current hard-block is the right default. What we should add is a clearly-documented, audit-trail-producing opt-in path for per-channel exposure, with the level of exposure gated by the agent's actual capabilities.**

### Why the current hard-block is right

The System agent has shell_exec, write_file, install_library, run_script, list_dir, read_file, all skills, all peers, all MCP servers, and a 30-minute budget. Exposing that on Telegram means:

- **Anyone in `allowed_user_ids`** (the existing Telegram allowlist at `adapter.go:54-94`) can send "delete everything in /home" and the agent will try. The allowlist is "who can talk to the bot," not "who can issue privileged commands" — and Telegram user IDs are not a strong identity. They're a long-lived integer that can be reused or socially-engineered.
- **No nuance per message.** The agent doesn't see "this message came from Telegram, so don't run shell." It sees a `message.Message` with `Channel: "telegram"` and proceeds with full capabilities. The only thing differentiating channels in the engine is `msg.Channel` as a string, never consulted for authorization.
- **Side-effects survive the session.** A web-GUI mistake can be caught by the operator watching the trace. A 7am cron-or-Telegram-triggered `rm -rf` happens unattended.
- **HMAC/replay surface.** WhatsApp's HMAC is replay-able (no nonce). Telegram's bot token is at the bot, not per-message. If any of these are leaked, the attacker has root on the host via shell_exec.

The reviewer's instinct ("warn but let them") doesn't survive the threat model. The cost of an unintended `shell_exec` call from a non-web channel is high enough that "we warned you" isn't proportionate. People install frameworks and accept defaults; defaults must be safe.

### What the warning would actually warn about — if we did it

If we allowed it with a warning, the warning has to be specific to be useful. A generic "this gives the agent more power" is the kind of UX nag everyone clicks through. Useful warnings would be:

- **Tool capability surface:** "This agent has `shell_exec`, `write_file`, `install_library`. Anyone allowed to message this Telegram bot can trigger these on the host." Listing the actual tools, not categories.
- **Peer reach:** "This agent can invoke `system-admin`, which itself has full filesystem access." Showing the transitive capability through peer agents.
- **MCP scope:** "MCP servers `filesystem`, `rocketmoney` are mounted with `*` access. The agent can call any of their tools without per-tool review."
- **Memory exfiltration:** "Memory `read_scopes` includes `global`. Anything ever written to memory by ANY agent — including financial data, secrets stored in conversations — is readable by this agent and could be leaked through a reply."
- **Sandbox limits:** "Tool subprocesses are sandboxed (cpu=30s, mem=512MB, fds=256) on Linux. On macOS, the memory limit is advisory." This sets expectations; doesn't fix anything.

That's five specific warnings, not one. The right UI for them is a per-bind diff at enable time, computed from the agent's actual definition, not a generic banner. That's a meaningful surface to build.

### The right middle ground

The structure I'd recommend:

**Three exposure tiers, classified per agent automatically.** Compute a capability tier from the agent's declaration at load time:

| Tier | Criteria | Default channel policy |
|---|---|---|
| **read-only** | No `builtins` matching `{shell_exec, write_file, run_script, install_library}`. No `system_tools: true`. No peer agent that recursively has any of the above. | Allowed on any channel by default. |
| **active** | Has `web_search`, `kb_search`, `read_file`, `list_dir`, or other read/external-but-not-write tools. May write to memory. No filesystem-write or process-spawning tools. | Allowed on web (http) by default. Opt-in per non-web channel with a typed confirmation. |
| **privileged** | Has any of `shell_exec`, `write_file`, `run_script`, `install_library`, OR `system_tools: true`, OR transitive peer with the above. | Web-only by default. Non-web channels require BOTH a config opt-in AND an explicit `accept_privileged_exposure: true` flag on the specific channel binding. The flag is a YAML-level "I have read what this exposes" stamp. |

This subsumes the current hard-block on the System agent (it'd land in `privileged` automatically because it has shell_exec). It also generalizes: any future agent that grows shell_exec inherits the same protection without needing a hardcoded ID check. The current `externalChannelAgentAllowed` becomes a special case of the tier check.

**The transitive check matters.** An "orchestrator" agent with no shell_exec itself but a peer of `system-admin` is just as dangerous as the System agent. Computing the tier requires walking the peer graph. The walk should bound at the existing `maxAgentCallDepth = 5` to match the runtime's reach.

**Per-channel UX, not generic.** Each adapter has different threat models:
- **Telegram** — `allowed_user_ids` is medium-trust. Tier check + show the user "this bot's agent has these capabilities" when they /start it.
- **Slack** — workspace-scoped, OAuth-based identity, but @-mentions can come from anyone in the workspace. Stricter check on `privileged` exposure: list workspace admins, require admin sign-off.
- **Discord** — similar to Slack but guilds are usually less curated. Default to `read-only`-only without explicit owner enable.
- **WhatsApp** — phone numbers as identity is low-trust (SIM swap exists). Block `privileged` outright. `active` requires the operator to enumerate `allowed_phone_numbers`.

### Recommendation summary for Q1

1. Generalize the System-agent hard-block (`internal/app/channels.go:101`) into a capability-tier check. The System agent is "privileged" because of shell_exec, not because of its ID.
2. Compute the tier from `def.Builtins + def.SystemTools + transitive(def.Agents).Builtins`. Cache at load time.
3. For non-web channels, require `accept_privileged_exposure: true` in the channel binding YAML when the bound agent is privileged. No GUI toggle that produces the same effect — make it a YAML-level decision so it's reviewable in the config file.
4. Surface the tier and the per-capability warning text in the GUI on the channel binding screen. The operator sees what they're enabling before they confirm.
5. Audit-log every non-web binding of a non-read-only agent with the agent's tier + a snapshot of its tool list at the time. So six months later, "when did we agree this Telegram bot could shell_exec?" is answerable.

---

## Q2 — channel↔agent connectivity model

### Where each channel sits today

| Channel | Platform constraint | Adapter shape | Mapping per bot/endpoint |
|---|---|---|---|
| **HTTP** | None (we own the protocol) | One adapter, `Receive(agentID, …)` per request | N:N — every request picks an agent at message time |
| **Telegram** | Bot token = bot identity; 1 bot = 1 identity | One adapter per `bots:` entry, each with a fixed `agentID` | Per bot: 1:1. Per process: N (bots) → N (agents). |
| **Discord** | Bot token = identity, but one bot can be in many guilds | One adapter per `bots:` entry | Per bot: 1:1 with the agent. Per guild: same agent serves all channels in that guild. |
| **Slack** | Bot user = identity, one bot can serve many channels/users in a workspace | One adapter per `bots:` entry | Per bot: 1:1 with the agent. Per workspace channel: same agent. |
| **WhatsApp** | Phone number = identity; the platform has no bot abstraction | One adapter per number; webhook-bound | True 1:1 with no escape at platform level |

Two genuine constraints:

- **WhatsApp's 1:1 is structural.** No clever wiring on our side gets around the platform.
- **Bot-token-per-agent is structural for the chat platforms** (one bot can't have multiple identities). But within a single bot, we ARE free to route different inbound messages to different agents — the platform doesn't care which Go code handles the message internally.

### Three abstraction options, evaluated

**Inbox model.** Each channel binding is an inbox; a router (rule-based or LLM-based) at the inbox decides which agent handles each inbound message. The bound `agentID` on the adapter becomes the router agent, not the worker agent. The router can be a special-cased Soulacy agent type (e.g. `kind: router`) whose only job is classification + dispatch.

- ✅ Solves WhatsApp's 1:1 limit cleanly: the one bound agent is the router, and it can dispatch to N workers via `agent__<id>` peer calls. We already have the peer-call machinery (`internal/runtime/engine.go:2884-`).
- ✅ Generalizes — same model works on every channel, including HTTP (the GUI can show "[router] → [resolved agent]" in the trace).
- ✅ Rule-based routing is cheap (`if /finance: financial-agent; elif /research: research-agent; else: system`). LLM-based routing is a single first-turn classification call.
- ⚠ Costs an extra LLM hop per message when the router is itself an LLM. Cheap on rule-based / regex.
- ⚠ The router introduces new failure modes: misclassification, runaway delegation. Mitigated by depth cap (already 5).
- ⚠ Adds a concept users have to learn ("what's a router agent?"). But it's a single concept that explains every channel.

**Mention model.** Multi-agent-capable channels (Slack, Discord) route by `@mention` or prefix. Single-agent channels (WhatsApp) get a single front-desk agent that can delegate via peers.

- ✅ Maps directly to user expectation on Slack/Discord: people already think in @mentions.
- ✅ Doesn't introduce a new "router" entity — the agent's own front-desk role and peer list does the work.
- ⚠ Doesn't actually solve the 1:1 problem; it just rebrands it. The front-desk agent IS the router; calling it a "front desk" is cosmetics.
- ⚠ Different UX on different channels. Operator has to know that Slack uses @, WhatsApp uses an implicit single agent, Telegram uses /commands. Higher cognitive load.
- ⚠ The @mention parsing has to handle adversarial input (`@all`, ambiguous names). Real work for marginal benefit.

**Workspace model.** Agents grouped into "workspaces"; each channel binds to a workspace; the workspace has a default agent + optional routing.

- ✅ Matches how Slack/Discord users think about scope.
- ⚠ Introduces yet another concept (workspace vs agent vs channel). For a single-operator self-hosted runtime, this is over-modelling.
- ⚠ Doesn't really solve the multi-agent-per-channel problem unless the workspace itself has a router — at which point it's the inbox model with extra steps.
- ⚠ Most useful for multi-tenant SaaS deployments, which Soulacy isn't (and isn't claiming to be).

### Recommendation summary for Q2

**Adopt the Inbox model. Make the router explicit. Treat it as the universal abstraction across every channel.**

Concrete shape:

1. **Channel bindings target a single agent (unchanged).** The current per-adapter `agentID` field stays. We do NOT try to make adapters multi-target. The 1:1 binding is honest and matches every platform's constraint.

2. **Allow that single agent to be of kind "router."** Add an optional `kind: router` field to `agent.Definition`. A router's behavior:
   - It runs an LLM-or-rule classification on the first turn to pick a worker agent from its `agents:` peer list.
   - It delegates via the existing `agent__<id>` peer-call mechanism — no new runtime path.
   - Its own reply is the worker's reply, unwrapped.
   - The trace shows `router=<id>, resolved=<workerID>` so the operator can debug misclassification.

3. **Pre-built `brain-router` already exists** (it's in the System agent's peer list — `examples/agents/system/SOUL.yaml:45`). Promote it to a first-class concept by giving it the `kind: router` declaration. Users who want N agents on Telegram set their bot's `agent_id` to `brain-router` (or their own custom router), and the router fans out.

4. **For HTTP, keep per-request agent selection as-is.** The web GUI and API callers already have a session-scoped agent picker. A router is one option, not the default. (This preserves backward compatibility and matches user expectation that the GUI lets them pick.)

5. **Rule-based router as the first implementation, LLM-based as opt-in.** A SOUL.yaml block like:
   ```yaml
   kind: router
   routes:
     - if: "starts_with(/research,/r)"   → research-agent
     - if: "contains(/finance,$,money)"  → financial-agent
     - if: "contains(/shell,!,exec)"     → system-admin   # explicit privileged invocation
     - else: writing-agent
   ```
   No LLM call needed for routing if the rules match. Falls back to LLM classification only when explicitly configured.

6. **Make WhatsApp's 1:1 = "always router."** Default WhatsApp bindings to require either a non-router agent (if the operator wants exactly one agent on that number) OR a router (if they want fan-out). Document this as the canonical fan-out pattern; it's the only one that works under WhatsApp's structural limit.

7. **Inbox model + capability tiers compose.** The router itself has a capability tier — it can only delegate to agents whose tier the channel allows. A `privileged` worker is unreachable from a Telegram router unless the router AND the channel both opted in. This collapses Q1 and Q2 into one consistent rule: capability is per-agent, exposure is per-binding, the router is the dispatch mechanism.

### What we explicitly don't do

- **No multi-agent-per-adapter.** Each adapter stays 1:1 with one agent. That agent may happen to be a router that dispatches to N peers. We don't change the adapter contract.
- **No magic LLM router as default.** Defaults are rule-based. The LLM router is an opt-in for users who want it; the cost (extra LLM call per message) shouldn't be hidden.
- **No new "workspace" or "tenant" concept.** Two layers (agent, binding) plus a router-as-agent specialization is the cap. More layers buy us nothing for a single-operator runtime and confuse the model for multi-tenant deployments we're not building.

---

## How Q1 and Q2 combine

The two questions look separate but converge:

- The System agent's exposure problem (Q1) is the same as the fan-out problem (Q2) seen from the other side. Right now, Telegram has at most one agent per bot; the System agent isn't allowed to be that agent because it's too privileged.
- With the inbox model: a Telegram bot is bound to a router. The router can dispatch to the System agent ONLY IF the System agent's capability tier is allowed on that channel binding. So the same tier mechanism gates both questions.
- The user-facing story collapses to: "Each channel binding is a router. Each agent has a capability tier. Some tiers are blocked on some channels by default. Opt-in is a typed YAML flag." Three concepts. Composes naturally.

The work order I'd suggest, if we commit to this design:

1. Capability-tier computation + the tier-based replacement for `externalChannelAgentAllowed`. ~150 LoC. Closes Q1's default-deny correctness gap. **— Implemented.** Tier classifier at `internal/tier/tier.go:51-`. Binding gate at `internal/app/channels.go::bindingDecision`. Wire.go call sites swapped at `internal/app/wire.go:774,800,829,852,882,906,931,985,1012`. Tier-aware structured logging built into the gate (one log line per binding showing tier + decision).
2. `kind: router` agent type + rule-based dispatch. ~250 LoC. Solves Q2 for the chat platforms. **— Implemented.** New `Kind` + `Routes` fields on `pkg/agent/types.go::Definition`, new `RouterRoute` struct. Engine short-circuit at `internal/runtime/engine.go::Handle` after the System-agent guard; dispatcher + pure rule-matcher at `internal/runtime/engine.go::dispatchRouter` / `::pickRouterRoute`. Brain-router promoted to `kind: router` with concrete routes at `examples/agents/brain-router/SOUL.yaml`.
3. LLM-based router as opt-in. ~100 LoC after (1)+(2). Pure addition. **— Deferred.** Rule-based covers the common case; the LLM fallback is a fast-follow once rule-based routing has real-world traffic to compare against. Tracked as TODO at the end of this section.
4. GUI surfaces: tier badge on each agent, "this binding will expose tier X" diff on channel-bind screen. ~100 LoC Svelte. **— Deferred to a follow-up.** Backend now emits the tier in the structured log line per binding, so operators can see the new gating in `/tmp/soulacy.log` without GUI work. GUI presentation is pure UX polish; not blocking the security improvement.
5. Audit-log every privileged-channel binding decision. ~30 LoC. **— Implemented as part of (1).** Folded into `bindingDecision`: every call emits one `Info` (read-only/active) or `Warn` (privileged) log line with `adapter_id`, `channel`, `agent_id`, `tier`, `decision`, and (for Privileged) a `hint` field. Grep `"channel binding"` in `/tmp/soulacy.log` for the audit trail.

Total: roughly 600 LoC for a coherent answer to both questions. None of it is throwaway — the tier system pays for itself the first time a future agent grows shell_exec without anyone noticing the implication.

### Deferred follow-ups (write these up before forgetting)

- **TODO: LLM-based router fallback (item 3).** Add `kind: router` + a special route entry like `{ llm_classifier: true, target: "<peer-id>" }` so when no rule-based route matches, the router runs a tiny classification prompt to pick a peer. Useful when rule sets get unwieldy. Bounded to ONE LLM call (no loop) before dispatch.
- **TODO: GUI tier badges + binding diff (item 4).** Surface `tier.Compute(def, …)` as a coloured pill on the Agents list and the Agent Edit header. On the Channels page, show "this binding will expose tier X" with the per-capability list (`shell_exec`, `write_file`, etc.) before the operator confirms. Backend is ready; just needs Svelte.
- **TODO: walk-time capability-tier audit at config save.** When an agent's tier changes (e.g. an operator adds `shell_exec` to its `builtins:`), emit a one-time audit event so any existing non-web bindings to that agent become visible as newly-privileged. Today the tier is computed at process start; mid-run changes only land on next restart.
- **TODO: per-route metric.** Add a Prometheus counter `router_route_hits_total{router, target, match_kind}` so operators can see routing distribution over time. Cheap addition, helps debug misclassification.

---

## Closing observation

The thing the current code does right that's worth preserving: **policy lives in declarative places (YAML + config), not in the engine path.** The `externalChannelAgentAllowed` guard at `channels.go:101` is one line of policy, easy to read, easy to evolve. Don't bury the new tier check inside the engine's hot path — keep it at adapter construction time, fail loudly, log the decision. The engine should still be "build context, call LLM, execute tools" with no per-channel branches. All the channel-aware policy belongs at the wire-up boundary.
