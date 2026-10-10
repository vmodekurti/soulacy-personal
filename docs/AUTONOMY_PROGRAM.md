# Autonomy — program

Soulacy can already reason, plan, remember, schedule, and delegate. What it
mostly cannot do is *act on the world*. This document is the build plan for
closing that gap without giving up the property the whole project is built on:
for any action an agent ever takes, you can answer one of two questions — who
approved it, or which line permitted it.

It is written to be executed slice by slice. Each slice is usable on its own
and leaves the runtime safer than it found it.

## What counts as an effect

An effect is anything an agent does that the world remembers. Seven classes,
each with its own reversibility profile and its own budget unit. The whole
program is framed around this table; nothing here is specific to shopping.

| Class | Examples | Typically | Budget unit |
|---|---|---|---|
| **Communicate** | send a message, email, reply, post, calendar invite | compensable at best — people saw it | messages |
| **Commit** | book, reserve, RSVP, schedule with someone else | compensable (cancel) | commitments |
| **Spend** | purchase, transfer, subscribe | irreversible | currency |
| **Mutate** | write, update or delete a file, record, document | reversible with a receipt | writes, deletes |
| **Configure** | change a setting, install, grant access, rotate a key | often irreversible, wide blast radius | config changes |
| **Actuate** | unlock a door, set a thermostat, start a car | irreversible in the moment, safety-critical | actuations |
| **Compute** | run code, shell, spawn a process | depends entirely on what it does | already covered by capability tiers |

For a personal assistant **Communicate is the class that matters most**, not
Spend. An agent that sends a message in your name, to the wrong person, saying
something you would not have said, has done more damage than one that buys the
wrong thing: money comes back and a message does not. Effectors, approvals and
budgets are designed around that first.

## Principles

1. **Autonomous up to the last irreversible step.** Research, compare, draft,
   stage, fill the cart, prepare the transfer — all unattended, all day, no
   approvals. The irreversible act is where a human belongs, and that stop is
   a feature, not a limitation we intend to remove.
2. **Budget before hands.** An effector without a spend ceiling is a liability
   you have to babysit, which means you never leave it running, which means you
   never learn what autonomy actually needs. Every capability that touches the
   world lands *after* the budget that bounds it.
3. **Every effect declares its reversibility.** `reversible`, `compensable`, or
   `irreversible` is a property of the tool, checked by the runtime. "Ask a
   human before anything irreversible" must be structural, not per-agent
   configuration an author can forget.
4. **An approval a human will actually read.** An agent running all day
   produces more approvals than anyone reads one at a time. They must carry
   meaning (what, how much, to whom), batch, and expire safely.
5. **Silence is the worst failure.** A wrong action is recoverable and visible.
   A loop that burns budget and says nothing is neither. Every autonomous run
   owes an escalation when it is stuck.

## Where we start from

More of this exists than the gap suggests. Already shipped:

- **Goals as a bounded multi-agent DAG** — validated and persisted atomically,
  dependency-ordered, crash-safe resumption (`internal/autopilot/goal.go`).
- **Exactly-once claims before external effects** — `ClaimRun` durably claims a
  subject/run ID before anything leaves the box, and crash-uncertain work is
  listed for recovery rather than silently replayed (`autopilot/claim.go`).
- **Mission contracts** — deterministic checks over trusted run observations,
  fail-safe even when validation was skipped (`autopilot/mission.go`).
- **Proposals** — a candidate change is accepted only with regression evidence
  against a baseline (`autopilot/proposal.go`).
- **Nested execution budgets for inference** — `WithExecutionBudget` gives
  parallel and nested agents shared ancestor reservations; a child may tighten
  but cannot reset its parent's budget (`internal/llm/execution_budget.go`).
  The cost governor can already return `ConfirmationRequiredError` *before*
  provider access.
- **Capability tiers** computed transitively through peer agents
  (`internal/tier`), the intent gate, the tool sandbox, approvals with
  deadlines, Safe Undo for two resource kinds, and run proofs.

The muscle and the interlocks are what is missing.

## Slices

### Slice 1 — Bound the blast radius

Nothing here lets an agent do anything new. It makes everything that follows
safe to leave alone overnight.

| Story | Runtime | Surface |
|---|---|---|
| **A1 Effect budget** | A world-budget mirroring `executionScope` semantics: per run and per period, counting money, messages sent, external writes, and destructive operations. Nested scopes; a child tightens, never resets. Exhaustion refuses the call with an actionable error, the way token admission already does. | `effects:` block in `SOUL.yaml`; per-agent and workspace defaults in `config.yaml`; remaining budget visible on the run. |
| **A2 Reversibility class** | Every tool (builtin, MCP, skill, plugin) declares `reversible \| compensable \| irreversible`. Unknown counts as irreversible. The runtime refuses an irreversible call from an `unattended: true` run unless the agent's file names that exact tool. | Tier and agent pages show what an agent can do that cannot be taken back. |
| **A3 Spend and effect in run proofs** | Autopilot proofs record effects attempted, effects applied, and budget consumed, next to the existing checks and cost. | Autopilot → Run proofs gains an "effects" column. |

### Slice 2 — Approvals worth reading

| Story | Runtime | Surface |
|---|---|---|
| **A4 Semantic approval payload** | Approvals carry a structured summary the agent must produce. `action`, `target` and a one-line plain-English `summary` are required for every class; the rest is class-specific (`amount` for Spend, `recipient` and `visibility` for Communicate, `count` and `scope` for Mutate, `duration` for Actuate). Malformed or missing summary on an irreversible call fails closed. | Web, iPhone, and watch render the summary first and the arguments on demand. |
| **A5 Batch review** | The broker groups pending approvals by agent and run so a day of staged work can be reviewed together, approved or denied as a set, with per-item override. | Inbox gains a grouped view; one Face ID for a reviewed batch. |
| **A6 Expiry that fails safe** | Every approval already has a deadline. Make the timeout policy explicit per tool: expire-deny (default) or expire-skip, recorded in the proof either way. | Deadline and policy shown on the approval. |

### Slice 3 — Hands

Effectors land only after Slices 1 and 2, and each one arrives with its
reversibility class and its budget cost.

| Story | Runtime | Surface |
|---|---|---|
| **A7 One effector, worked end to end: send a message** | The first effector is Communicate, because it is the one people reach for first and the one with the least forgiving failure. A drafted reply is staged, the approval shows recipient, channel and full text, and nothing leaves until a human says so. Establishes the pattern every later effector follows. | A worked example agent plus the docs page for it. |
| **A8 Effectors where a real API exists** | Calendar write (Commit), file and record write (Mutate), settings and access changes (Configure) — each arriving with its reversibility class, its budget unit, and a compensating action where one exists. | Skills/MCP pages list each effector with what it can and cannot undo. |
| **A8b Browser effector for sites with no API** | A vetted `@playwright/mcp` configuration and a persistent profile the owner logs into by hand, so the gateway never holds the password and an expired session fails by stopping. The worked example fills a cart and stops before checkout. | Template + docs page; the profile's login state is visible and resettable. |
| **A9 Compensating actions** | `compensable` tools register how to reverse themselves (cancel the order, delete the message, restore the file). Safe Undo's receipt model extends to cover them. | Safe Undo lists compensable effects alongside today's two resource kinds. |

### Slice 4 — Keep going without going silent

| Story | Runtime | Surface |
|---|---|---|
| **A10 Retry policy and circuit breakers** | Per-tool retry with backoff; a flapping tool opens a circuit and the run reports rather than spins. Budget is consumed by attempts, so a retry storm ends by construction. | Run trace shows attempts, backoff, and open circuits. |
| **A11 Stuck escalation** | A run that cannot progress raises an inbox item naming what it tried, what it is waiting on, and what it would do with permission. | Inbox: "stuck" is a first-class decision type beside approvals. |
| **A12 Standing goals** | A goal that outlives a run ("nobody I owe a reply to waits more than two days", "every renewal gets flagged a week out"): success criteria as a mission contract, re-evaluated when the person model or a watched signal changes, able to wait days and resume. | Autopilot gains goals that persist, with their last evaluation visible. |

### Slice 5 — A second opinion

| Story | Runtime | Surface |
|---|---|---|
| **A13 Independent pre-flight check** | Before an irreversible effect, a separate evaluator (a different model, or deterministic rules) answers: does this action match the stated goal and what we know about the person? Disagreement escalates to a human; it never silently allows. | The check and its verdict appear in the approval and the proof. |

## Non-goals

- **Removing the human from irreversible actions.** Autonomous checkout,
  payments, and transfers stay opt-in, gated behind `unattended: true`, an
  explicit tool grant, and a spend cap. The default never changes.
- **Storing payment credentials.** The gateway holds no card numbers. An
  effector uses a payment method already saved with the merchant, or it does
  not run.
- **Physical-world actuation.** Locks, vehicles, thermostats and anything else
  where a wrong action is a safety question rather than a money question. The
  Actuate class is named here so the budget and reversibility work accounts for
  it, but shipping actuators needs its own program and its own review.
- **A general-purpose autonomous web agent.** Browser automation is for sites
  with no API, scoped per agent, not a licence to roam.
- **Autonomy that cannot be explained afterwards.** Any capability that would
  leave "what did it do overnight, and who allowed it" unanswerable does not
  ship, however useful.

## Ordering note

Slice 1 before everything. The instinct is to add hands first and controls
later; that order produces a system nobody dares leave running, and the whole
value of autonomy is what happens while you are asleep.
