# Design: Task-aware model roles & a single model resolver

Status: proposal · Owner: TBD · Last updated: see git

## 1. Problem

Soulacy currently treats "the LLM" as one global thing, but different tasks
need different models, and the wiring that picks a model is duplicated and
inconsistent across the codebase. There are at least **four independent
"get me a model" paths**, each constructed differently and each able to break
on its own:

| Path | Where | How it resolves the model | Gets the provider's `base_url`/key? |
|------|-------|---------------------------|--------------------------------------|
| Classic chat / tool loop | provider registry (`internal/llm`, wired in `wire_subsystems.go`) | registered provider instance | yes (from config/env) |
| Reasoning loop (ReAct / Plan-Execute) | `internal/reasoning.DefaultBackendFor` | builds its **own** backend from `def.LLM.BaseURL` | **no** — empty agent base_url → `localhost:11434` |
| Studio compiler / agent builders | `gateway.studioLLM()` | separate selection | partial |
| Embeddings | `wire_subsystems.go` embedders | its own Ollama base_url | yes |

Because these are separate, fixing the provider in `config.yaml` fixes one path
but not the others. Concrete failures observed:

- **ReAct "loop produced empty output"** — the reasoning backend called
  `http://localhost:11434` inside the container (agent `base_url` was empty, so
  `DefaultBackendFor` fell back to `defaultOllamaBase`). The classic loop worked
  because it used the registered provider with `host.docker.internal`. Same
  model, different code path, different result.
- **Studio inventing tool names / ReAct empty output on small models** — the
  compiler and reasoning planner ran on whatever the agent used (e.g. a small
  local model), which is unreliable at structured-JSON / planning tasks. These
  tasks need a stronger model than the one that *runs* the agent.

The root cause is architectural: **model selection is ad hoc and not aware of
the task it's selecting for.**

## 2. Goals

1. One central resolver that every code path uses to obtain a model client —
   always applying the configured provider's `base_url`, key, and options. This
   makes the localhost-style bug structurally impossible.
2. **Task-aware roles**: the model that *builds* an agent (Studio) or *plans*
   for it (reasoning) is chosen independently of the model that *runs* it.
3. Smart defaults that "just work," surfaced to the user, fully overridable.
4. GUI and CLI configuration for all of it.

Non-goals: changing provider adapters themselves, or the prompt content of any
task (covered elsewhere).

## 3. Model roles

Introduce a small fixed set of **roles**. A role is a named slot that resolves
to `{provider, model, base_url?, options?}`:

| Role | Used by | Needs |
|------|---------|-------|
| `chat` (default) | agent conversations + classic tool loop | cost-sensitive; local is fine |
| `reasoner` | ReAct / Plan-Execute planning & reflection | reliable structured JSON, decent reasoning |
| `studio` | Studio compiler, agent/skill builders, suggestions | strong instruction-following + JSON |
| `embedder` | knowledge / vector embeddings | embedding model (already separate) |

Resolution order for any role (first hit wins):

1. **Per-agent override** (SOUL.yaml `llm:` / `reasoning:` block) — only for
   `chat`/`reasoner` where an agent legitimately overrides.
2. **Explicit role config** in `config.yaml` (`llm.roles.<role>`).
3. **Role default** derived by detection (§5).

Every resolution returns a client built by the **same** central resolver, so it
always carries the provider's `base_url` + key.

## 4. Central resolver

A single function (sketch):

```go
// package llm (or a new package modelresolver)
type Role string
const (
    RoleChat     Role = "chat"
    RoleReasoner Role = "reasoner"
    RoleStudio   Role = "studio"
    RoleEmbedder Role = "embedder"
)

// Resolve returns a ready-to-use client for the role, with provider base_url,
// api_key and options already applied. def may be nil for non-agent roles
// (studio/embedder) or carry per-agent overrides (chat/reasoner).
func (r *Resolver) Resolve(role Role, def *agent.Definition) (Client, ResolvedModel, error)
```

`ResolvedModel{Provider, Model, BaseURL}` is also returned so callers can log
and surface it ("compiling with gemini-2.5-pro"). The reasoning loop,
`studioLLM()`, and the chat loop all call `Resolve(...)` instead of constructing
backends by hand. `DefaultBackendFor`'s hand-rolled Ollama construction is
deleted.

This step alone fixes the ReAct localhost bug: the `reasoner` role resolves
through the provider config that already has `host.docker.internal`.

## 5. Defaults & detection

The resolver computes role defaults from what's actually available, so most
users never configure anything:

- **Detect cloud keys**: if a provider with a key exists (anthropic, openai,
  google/gemini, groq, …), it's a candidate for the "strong" roles.
- **Detect local models**: query Ollama `/api/tags` for installed models.
- **`chat`** → the user's `llm.default_provider` (unchanged behavior).
- **`studio`/`reasoner`** → prefer the strongest configured cloud model; else
  the best installed local model (e.g. a 70B / qwen-class model) for JSON
  reliability. **Never** silently fall back to an unreachable local default.

Rationale, from observed failures: run agents on cheap local models if you
want, but compile/plan with your strongest model — the build-time model and the
run-time model have different requirements.

## 6. Config schema

Additive and backward-compatible (`llm.default_provider` keeps working as the
`chat` default):

```yaml
llm:
  default_provider: ollama          # = chat role default (unchanged)
  providers:
    ollama:   { base_url: "http://host.docker.internal:11434", model: "llama3:70b" }
    google:   { api_key: "...", model: "gemini-2.5-pro" }
  roles:                            # NEW — all optional; detection fills the gaps
    reasoner: { provider: google, model: "gemini-2.5-pro" }
    studio:   { provider: google, model: "gemini-2.5-pro" }
    # chat / embedder omitted -> defaults
```

## 7. Guidance: smart → surfaced → override

1. **Auto** — detection picks per-role defaults; most users do nothing.
2. **Surfaced** — show the choice. Extends the Studio recommendation banner
   ("compiling with gemini-2.5-pro; agent runs on llama3:70b") and a per-run
   note in chat/reasoning.
3. **Override** — `llm.roles.*` in config, per-agent in SOUL.yaml.

## 8. GUI & CLI surfaces

- **GUI** — a "Models" section on the Config page (same pattern as the new
  web-search control and the default-provider dropdown): one row per role →
  provider dropdown + model dropdown, validated against registered providers /
  pulled models. Backend: add `Roles` to `PatchableConfig` + `applyPatch`, and
  expose `llm.roles` in `safeConfigView`.
- **CLI** — an `sy onboard` step ("Which model should build agents and do
  reasoning?") and `sy config set llm.roles.<role>.{provider,model} …` for
  scripting.
- **Per-agent** — the Agents page already exposes provider/model/strategy; add
  `base_url` so an agent can't silently fall back to localhost, and a warning
  when an agent has both a `workflow:` and a `reasoning.strategy` (the workflow
  silently wins — see the Studio mode-recommendation work).

## 9. Phased rollout

1. **Central resolver + `reasoner`/`studio` role defaults** (highest leverage):
   fixes the ReAct localhost bug *and* routes Studio/agent-building to the
   strongest available model. Delete `DefaultBackendFor`'s hand-rolled Ollama
   path; route `studioLLM()` and the reasoning loop through `Resolve`.
2. **`llm.roles` config schema + detection** — make defaults explicit and
   overridable in `config.yaml`.
3. **GUI Models section** — provider/model dropdowns per role on the Config
   page (backend `Roles` patch support + a Config.svelte panel).
4. **CLI** — `sy onboard` step + `sy config set llm.roles.*`.
5. **Per-agent base_url + workflow/strategy conflict warning.**

## 10. Risks & notes

- **Backward compatibility**: `llm.roles` is additive; absent → current behavior
  (chat from `default_provider`, reasoner/studio from detection). No migration
  required.
- **Reachability validation**: detection must verify a chosen model is actually
  reachable/pulled (boot probe already exists for agents) and degrade with a
  loud log rather than a silent empty-output failure.
- **Cost surprise**: defaulting studio/reasoner to a cloud model when a key
  exists could surprise cost-sensitive users — hence "surfaced" guidance and an
  easy override.
