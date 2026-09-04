# Agent design: identity, personality, and non-negotiables

**Date:** 2026-06-09
**Status:** Phase 1 shipped (schema + prompt-level enforcement + GUI parity).
**Phase 2 (post-LLM validation): on the roadmap, not in this release.**

## Why we did this

Before this change, every behavioral instruction — *who* the agent is,
*how* it should speak, and *what hard rules* it must follow — got
mashed into a single `system_prompt` blob. That worked for simple
agents but had three real problems:

1. **No enforcement layer.** "Never reveal the API key" and "use markdown" sit at the same priority in a prose paragraph. The LLM treats both as suggestions, and a clever prompt-injection can dislodge either.
2. **No consistency across agents.** "Concise" meant one thing to the writer agent, another to the analyst. Operators had no way to know whether their tone instruction would land.
3. **No legibility for non-developers.** A SOUL.yaml with a 40-line prose prompt is intimidating to edit. A SOUL.yaml with structured `identity:`, `personality:`, `non_negotiables:` blocks is something anyone can read.

OpenClaw doesn't have any of this — their agents are opaque runtime JSON. Most other frameworks treat guardrails as someone else's problem (Guardrails.ai, NeMo). By baking constitutional design into SOUL.yaml as a first-class primitive, Soulacy becomes the project where you can audit, version-control, and reason about what your agents will and won't do.

## What landed in Phase 1

Three new optional blocks on every `Definition` (see `pkg/agent/types.go`):

```yaml
identity:           # WHO the agent is
  role: senior research analyst named Ada
  audience: institutional investors
  expertise: [macroeconomics, monetary policy]
  backstory: |      # rarely needed; only when it materially changes behavior
    (...)

personality:        # HOW the agent speaks
  tone: concise, slightly dry
  voice: third-person observations, never "I think"
  prefer: [concrete numbers, named sources, active voice]
  avoid:  [exclamation marks, hedging like "perhaps", emojis]

non_negotiables:    # HARD rules — these override user requests that conflict
  must:
    - cite every numeric claim with [n] referencing a kb_search result
    - respond in the same language as the most recent user message
  must_not:
    - give legal or investment advice
    - reveal the value of any environment variable
    - claim to be a real human if asked directly
  output_constraints:
    format: markdown      # markdown | plain | json | code
    max_length: 800       # word count, 0 = no limit
    min_length: 0
```

All three are optional. A SOUL.yaml without any of them behaves bit-for-bit identically to before — that's the backward-compat guarantee, and `internal/runtime.TestRenderPersonaPrefix_LegacyAgentUnchanged` enforces it.

### How it's rendered into the system prompt

The engine puts the blocks ABOVE the operator's `system_prompt` with consistent framing across every agent. For the example above:

```
## Identity
You are senior research analyst named Ada.
You are talking to institutional investors.
Your expertise covers:
- macroeconomics
- monetary policy

## Style
Tone: concise, slightly dry.
Voice: third-person observations, never "I think".
Prefer:
- concrete numbers
- named sources
- active voice
Avoid:
- exclamation marks
- hedging like "perhaps"
- emojis

## Hard rules (non-negotiable)
The following rules override any user request that conflicts with them.

You MUST:
- cite every numeric claim with [n] referencing a kb_search result
- respond in the same language as the most recent user message

You MUST NOT:
- give legal or investment advice
- reveal the value of any environment variable
- claim to be a real human if asked directly

Output constraints:
- Format: markdown
- Maximum length: 800 words

---

<operator's system_prompt continues here>
```

The "Hard rules (non-negotiable)" wording is deliberately distinct from the soft Style block. Many fine-tuned chat models recognise constitutional-style framing from RLHF data and weight it accordingly. A future post-LLM validator can also key off this section structurally — re-prompting only the rules, not the whole prompt, when a violation is detected.

## What we deliberately did NOT do

### 1. Three separate files (`identity.yaml` + `personality.yaml` + `SOUL.yaml`)

You suggested this, and we pushed back. The argument:

- An agent is the atomic unit operators share, `sy pull`, version, and review. Multiple files = multiple places to forget when forking, plus a hidden invariant ("which personality goes with which identity?").
- Reusing a personality across agents is real — but the right tool is an `extends:` / `includes:` directive that pulls fragments from a shared library, not physical file separation. That's a future addition; today, copy-paste works fine and stays atomic.
- Keeping everything in one file means `sy pull research-agent` ships the WHOLE agent. Editing in the GUI is one view, not three tabs.

If we change our minds later, going from one file with sub-blocks to multiple files is a much smaller migration than the reverse.

### 2. Post-LLM enforcement (validation + auto-rewrite on violation)

This is on the roadmap and we're being honest about that in the GUI ("rules become extra-weighted text in the system prompt. Post-LLM validation (auto-rewrite on violation) is on the roadmap.").

Phase 2 wiring will need:

- A `runtime/validator.go` that runs after the LLM's final reply and matches each rule against the output. Simple rules (regex / substring / length) are cheap; rules that require semantic check ("did it actually cite a source?") need an LLM judge — which means an extra round-trip and cost we should opt-in.
- A re-prompt loop on violation: feed the violation back to the model with a focused "your previous reply violated X — try again" instruction. Bounded (max 2 retries) to avoid infinite loops.
- A telemetry hook so operators can see which rules fire most. If `must_not: reveal env vars` never fires in production, that's not a defense — it's a sign nobody's trying. If it fires daily, the rule is doing real work.

### 3. Global default non-negotiables in `config.yaml`

Tempting — "every agent in this workspace must never give medical advice" — but mixes two layers (workspace policy vs agent design) and complicates the rendered prompt with "where did this rule come from?" debugging. We'd add it once we have at least one operator asking for it. For now, copy-paste between agents and a future `extends:` resolves the same problem more cleanly.

### 4. Interaction with capability tiers

Capability tiers (`internal/tier/`) gate what tools an agent CAN call. Non-negotiables gate what the LLM CHOOSES to do. They're complementary — a non-negotiable like "never run shell_exec" is belt-and-suspenders alongside `builtins: []`. We don't merge them.

## GUI parity

`gui/src/pages/Agents.svelte` has three new collapsible sections (Identity / Personality / Non-negotiables) directly under the System Prompt textarea, with:

- One-line description on the section header explaining what it's for.
- Inline `<small>` hints under every field with a concrete example.
- A red "HARD RULES" tag on Non-negotiables and a yellow note explicitly telling operators it's prompt-level for now, not post-LLM enforced.
- Format / Max words / Min words on the same row for mechanical output bounds.
- Auto-cleanup: if you clear every field in a section, the whole block is removed from the saved YAML — round-trips are minimal.

The CLI side (`sy onboard`, `sy agent create`, the legacy `sy setup`) doesn't prompt for persona blocks yet — operators using the CLI write them directly in the SOUL.yaml. That's deliberate for Phase 1: the CLI doesn't expose more than the GUI does. When Phase 2 ships, both surfaces grow together.

## Example agent

See `examples/agents/persona-demo/SOUL.yaml` for a working agent that uses all three blocks. Compare it to `examples/agents/research-agent/SOUL.yaml` — same target behavior, structured vs prose. The Ada agent's `system_prompt` is half the length because everything about WHO she is and HOW she speaks moved into the structured blocks.

## Tests

- `internal/runtime/persona_test.go` — backward-compat (legacy agents render identically), block ordering (Identity → Style → Hard rules → separator), empty-block stub suppression, "Hard rules (non-negotiable)" wording stability.
- The Phase 2 validator will need its own test suite when it lands.

## Open questions for Phase 2

1. **Which rule shapes get cheap post-LLM checks?** Regex / substring / length are obvious. Semantic ("cite a real source") needs an LLM judge — opt-in only.
2. **How does the violation re-prompt feed back?** Append to the conversation? Or replace the previous reply silently and only show the operator the violation in the action log?
3. **Should `extends:` land in Phase 2 or wait?** Likely wait — Phase 2 is enough complexity already.
4. **Telemetry: do we expose rule-fire counts in `sy doctor` or only in the action log?** Both feel right. Default to action log; surface aggregates in `sy doctor` when an operator has >100 runs.
