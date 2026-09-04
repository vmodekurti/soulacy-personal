# Vasu OS on Soulacy — Feasibility

**Date:** 2026-07-05
**Scope:** Honest read on whether Soulacy today supports a 4-level
hierarchy: `Vasu OS` → domain head → specialist → sub-specialist →
terminal (KB / MCP tool). No implementation. Cited against current
`main`.

## TL;DR

You can build 80% of Vasu OS on Soulacy today with existing primitives
(`kind: router`, peer agents, `knowledge:`, `mcp_servers:`,
`ConfirmTools`, capability tiers). Three real ceilings would bite in
practice:

1. **`maxAgentCallDepth = 5`** (`internal/runtime/engine.go:4094`) is
   tight. `Vasu OS → Head → Specialist → Sub-specialist → peer call`
   is already depth 4; one more hop and the engine refuses with an
   "infinite loop" error. Not fatal — but it constrains where the
   hierarchy can branch.
2. **Peer replies are flattened text**, not structured. `runAgentCall`
   returns `flattenParts(reply.Parts)` at line 4405. The Chief of
   Staff can't see raw KB hits from Research Agent — only the string
   the researcher composed. Round-tripping context up the tree is
   lossy by design.
3. **Peer dispatch is sequential.** No parallel fan-out.
   `internal/runtime/engine.go` uses one goroutine for peer calls
   (line 3569, `return e.runAgentCall(...)`) — no `errgroup`, no
   `WaitGroup`, no return-when-all. If Chief of Staff wants
   "research this AND check calendar in parallel", the engine
   serializes them.

None of these is a redesign. Below, section 3 lists the smallest
additions that would make the diagram a first-class pattern.

## 1. Can Soulacy support Vasu OS today?

Level by level, mapped to current primitives:

**Vasu OS (top-level entry point)** — two options, and the choice
depends on how much judgment lives here:

- **`kind: router`** (`internal/runtime/engine.go:1736`). No LLM, no
  cost, deterministic regex/prefix/contains dispatch to exactly ONE
  peer (`dispatchRouter` at line 4224). Perfect if inbound intent
  categorization is keyword-driven ("meeting notes" → Chief of Staff;
  "portfolio" → CIO Advisor). First match wins; supports a fallback
  route by omitting all match clauses on the last route.
- **Regular worker with `tool_choice: required` + `agents: [chief-of-staff,
  cio-advisor, personal-coach]`.** The LLM sees three peer tools and
  picks one on turn 1 (auto-delegate path at line 1970 skips the
  turn-1 LLM call when `tool_choice` names a specific peer). Costs
  one LLM round-trip per Vasu OS invocation, but handles ambiguous
  requests the router can't disambiguate.

**Domain heads (Chief of Staff, CIO, Coach)** — regular workers with
peers. Each declares its specialists in `agents: [...]`; the LLM sees
them as `agent__research-agent`, `agent__calendar-agent`, etc. The
`Auto-delegate` optimization only skips turn 1 when `tool_choice`
pins ONE specific peer, so if the head might call any of several
specialists based on the request, it pays for a real LLM decision
turn. That's the correct cost — head-level judgment lives here.

**Specialists (Research, Writing, Investment, etc.)** — same shape as
heads. Peers below become their `agents:` list; direct tool
integrations (web_search, kb_search, MCP tools) live in `builtins:`
and `mcp_servers:`.

**Terminals in your diagram — "Knowledge Base", "GitHub Agent", "Home
Agent"** — these are NOT agents in Soulacy's model. They're two
different things:

- **Knowledge Base** = `knowledge: [name-of-kb]` on the specialist.
  The engine injects a KB catalog and the `kb_search` builtin
  (`pkg/agent/types.go:348`). Terminal in the sense that KBs are
  data, not agents.
- **GitHub Agent, Home Agent** = MCP servers, not Soulacy agents.
  You'd run a GitHub MCP server (or a Home Assistant one) and
  reference them via `mcp_servers: [github, home]` on the
  appropriate specialist. Tools appear as
  `mcp__github__list_issues`, `mcp__home__set_thermostat`, etc.

That distinction matters for the diagram. The "columns" bottom out
at TOOLS or KBs, not at agents-all-the-way-down. The diagram should
probably be read as: agent → agent → agent → tool/KB, not agent →
agent → agent → agent → tool.

## 2. Where the current implementation hurts

**Depth cap of 5.** `const maxAgentCallDepth = 5` at
`internal/runtime/engine.go:4094`. `agentCallDepth` increments each
time an agent is invoked from a tool handler; exceeding the cap
returns:

```
agent call depth limit (5) exceeded calling "X" — possible infinite loop
```

Vasu OS routing chain: Vasu OS (depth 0) → Chief of Staff (1) →
Research Agent (2) → Calendar Agent (3) → sub-peer (4) → sub-sub-peer
(5) → ERROR. In the diagram, the specialist row (Research / Writing
/ Investment) sitting at depth 2 is fine; the row below (Calendar,
Email, Health) at depth 3 is fine; but if any of THOSE call another
agent, you're at depth 4 and the fifth level errors. So a strict
4-column diagram works; a 5-level tree doesn't without lifting the
cap.

**Peer replies are strings.** `runAgentCall` at line 4405 returns
`flattenParts(reply.Parts)` — the whole reply collapsed to one
string. There's no structured payload channel, no citations passed
up separately from the summary, no way for the Chief of Staff to
inspect *what specific KB chunks* the Research Agent used. If
Vasu OS wants to render sources or verify claims, that has to happen
at the LEAF (Research Agent has to include citations in its text
reply), not at the head, because the head only sees flattened text.

**Wall-clock budget is bounded — this actually works.** Contrary to
one framing, chain deadlines DO propagate. At depth 0 the engine
stamps `withChainDeadline(ctx, now + caller.RunTimeout)` at line
4113, and `withChainDeadline` is a no-op at depth > 0 so the ancestor
deadline is preserved unchanged. Worst-case latency for a 5-level
chain is bounded by Vasu OS's `run_timeout`, not the sum of
per-level timeouts. This is the good news — it's already correct.
`docs/CHANNEL_DESIGN.md` covered this and it shipped.

**Parallel peer dispatch.** Orchestrators can set
`parallel_peer_calls: true`. When the LLM emits two or more unique
`agent__X` tool calls in the same turn, Soulacy runs those peer agents
concurrently and then returns results to the coordinator in the original
tool-call order. Normal tools remain sequential. This keeps the safe,
isolated peer-session model while avoiding additive latency for
independent fan-out.

**Auto-delegate is single-peer only.** The `tool_choice: agent__X`
optimization at line 1970 skips turn 1 only for a single named peer.
Multi-peer parallel dispatch would need different plumbing.

## 3. Smallest additions that make Vasu OS a first-class pattern

Prioritized by leverage-per-effort:

1. **Configurable agent-call depth.** Shipped as
   `runtime.max_agent_call_depth` in `config.yaml` with default `5`.
   Operators can raise it to `8` or `10` for deeper Vasu OS-style
   hierarchies while keeping recursion bounded by the same chain-wide
   run timeout. Risk: infinite-loop failures take slightly longer to
   manifest when raised — still bounded.

2. **Parallel peer dispatch with deterministic result ordering.** Shipped
   as `parallel_peer_calls: true` on the caller. The engine parallelizes
   only unique peer-agent calls, preserves the original result order, and
   leaves ordinary tools sequential. This turns "3-second head +
   3-second peer + 3-second peer" into roughly 5s without surprising
   existing agents.

3. **Structured payload channel between peers.** Shipped as
   `structured_peer_results: true` on the coordinator agent. Peer
   replies are returned in a `SOULACY_AGENT_RESULT` JSON envelope with
   `target_agent`, `ok`, `content`, and parsed `structured` payload
   when the peer itself returned JSON. Backwards compatible — absent
   flag keeps the current flat-text behavior. Remaining work is richer
   Studio affordance and packaged team templates that prompt reviewers
   and researchers to emit citation/confidence JSON.

4. **`kind: coordinator`** — a new agent kind that's like a worker
   but MAINTAINS SESSION STATE across multiple peer calls, so a head
   can call research, then calendar, then compose an email based on
   both. Today each peer call runs as a fresh session with no shared
   history (`SessionID: "agent-call-" + uuidShort()` at line 4392),
   so the head has to re-inject context on every call. A coordinator
   kind would let one "coordination session" thread state across
   its peer calls. **Verdict: not needed as a new kind — the head is
   already a worker that CAN carry context across peer calls
   naturally, because the head's OWN session persists between LLM
   turns and the peer replies land in that session's tool-result
   messages.** Coordination is emergent from tool-loop mechanics.
   Skip.

## 4. Concrete SOUL.yaml sketch

Three agents, showing where friction is inline.

**Vasu OS as router (deterministic, no LLM cost):**

```yaml
id: vasu-os
name: Vasu OS
kind: router
description: |
  Top-level dispatcher for Vasu's personal agent stack. Routes
  inbound requests to one of three domain heads by keyword.

agents: [chief-of-staff, cio-advisor, personal-coach]

routes:
  - contains: [meeting, calendar, email, draft, schedule]
    target: chief-of-staff
  - contains: [portfolio, investment, stock, market, watch]
    target: cio-advisor
  - contains: [workout, sleep, mood, habit, reflection]
    target: personal-coach
  - target: chief-of-staff   # fallback; must be last

trigger: channel
channels: [http]
enabled: true
```

Friction: router picks ONE peer. If a request straddles two heads
("draft an email to my broker about last week's market"), it goes to
whichever's keywords match first. **Fix:** promote Vasu OS to a
worker with `tool_choice: required` if disambiguation matters more
than latency.

**Chief of Staff as a worker with peers:**

```yaml
id: chief-of-staff
name: Chief of Staff
description: |
  Handles calendar, email, meeting prep, and follow-ups. Delegates
  to research when a task needs external facts, writer for prose,
  calendar/email specialists for platform operations.

agents:
  - research-agent
  - writer-agent
  - calendar-agent
  - email-agent

builtins: []   # peers-only orchestrator; no direct web_search etc.

# ⚠ FRICTION: no way to declare "call research + calendar in parallel"
# in a single request. Currently each peer call is sequential.

# ⚠ FRICTION: peer replies come back as flattened text. If research
# returned 5 KB citations, this agent sees only what research chose
# to include in its final string. Structured hand-off not supported.

trigger: internal   # invoked by vasu-os
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  tool_choice: ""  # let the model decide which peers to call

max_turns: 6   # bounded — 1 for initial plan, up to 5 for peer results
run_timeout: 90s   # ENTIRE nested chain shares this budget

system_prompt: |
  You coordinate specialists. When a task needs research, delegate.
  When it needs prose, delegate. Compose the final reply yourself
  from what your specialists return.
```

Friction inline: parallel dispatch missing; structured hand-off
missing.

**Calendar Agent as a leaf specialist (MCP terminal):**

```yaml
id: calendar-agent
name: Calendar Agent
description: |
  Reads and modifies Vasu's Google Calendar via MCP. Optimized for
  quick reads (list next 5 events) and precise writes (schedule
  30min with X on Y).

mcp_servers: [google-calendar]

# ⚠ FRICTION: mcp_servers is a REFERENCE to something configured in
# ~/.soulacy/soulspace/config.yaml under `mcp:`. The agent itself is
# not tightly coupled to WHICH calendar backend — swap to
# `outlook-calendar` MCP server in config and this agent still works.

builtins: [kb_search]   # in case a KB has meeting-prep notes
knowledge: [meetings-2026]

confirm_tools:
  - mcp__google-calendar__create_event
  - mcp__google-calendar__delete_event
  # reads don't need confirmation; writes do.

trigger: internal
llm:
  provider: openai
  model: gpt-4o-mini
  temperature: 0.2

non_negotiables:
  must:
    - always confirm event times in Vasu's local timezone before creating
    - cite the specific event ID when reporting on existing events
  must_not:
    - create events without a title
    - delete events without the user confirming via confirm_tools flow

max_turns: 3
run_timeout: 15s   # peer call, not the top-level budget
```

Friction inline: MCP indirection is a feature (swappable backends)
but the operator has to remember that `mcp_servers:` names must
match config, and mismatch fails silently at load-time (no MCP
tools appear, agent just doesn't call the tool it needed).

## Verdict

- **Build Vasu OS today?** Yes, at 3-level depth with sequential
  dispatch. It works, it's slow when heads fan out, and it can't
  pass structured context up the tree.
- **Cost to make it native?** Two changes worth doing:
  raise `maxAgentCallDepth` to 8, and add parallel peer dispatch
  when the LLM emits multiple `agent__*` tool calls in one turn.
  Both are contained in `internal/runtime/engine.go`; neither
  requires new schema.
- **Structured payload hand-off** is the biggest architectural
  question and probably deserves its own design doc rather than a
  drive-by. Punt until you've felt the "flattened text loses my
  citations" pain in real use.
- **Terminals** in the diagram should be understood as tools (KBs
  + MCP servers), not agents-all-the-way-down. The 4-level agent
  chain in the diagram is really a 3-level agent chain that
  terminates at tool integrations. That distinction saves you
  from trying to build agents for things that are actually just
  MCP servers.

## References cited

- `internal/runtime/engine.go:1736` — router short-circuit in `Handle`
- `internal/runtime/engine.go:1970` — auto-delegate for `tool_choice: agent__X`
- `internal/runtime/engine.go:4094` — `const maxAgentCallDepth = 5`
- `internal/runtime/engine.go:4113` — `withChainDeadline` at depth 0
- `internal/runtime/engine.go:4224` — `dispatchRouter`
- `internal/runtime/engine.go:4331` — `runAgentCall` (peer call machinery)
- `internal/runtime/engine.go:4405` — `return flattenParts(reply.Parts)`
  (the reason peer replies are strings)
- `pkg/agent/types.go:348` — `Knowledge []string`
- `pkg/agent/types.go:396` — `MCPServers *[]string`
- `docs/CHANNEL_DESIGN.md` — Q2 discussion of `kind: router`
- `docs/AGENT_DESIGN.md` — persona blocks (relevant to per-agent
  non-negotiables in the Calendar sketch)
