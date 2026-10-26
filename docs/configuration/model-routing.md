# Model routing and LLM observability

Soulacy can choose a model for each LLM step and move to another configured provider when an availability failure makes the first choice unusable. Routing is optional. Existing workspaces keep their fixed provider and model assignments until **Smart model routing** is enabled in Settings.

## Smart defaults

The normal Settings view exposes one switch and four preferences:

- **Balanced** favors smaller models for simple steps and stronger models for complex work.
- **Faster / lower cost** favors models whose configured names indicate a smaller or faster tier.
- **Best quality** favors stronger model tiers first.
- **Local / private** only admits providers marked as local. It does not silently fall through to a cloud provider.

When no explicit route table is configured, Soulacy builds deterministic candidate lists from each configured provider's default model. It classifies a step as `simple`, `standard`, or `complex` from the execution phase, context size, requested reasoning effort, tool count, and structured-output requirements. Model-name ranking only orders compatible candidates; it does not weaken policy checks.

An agent can inherit the workspace behavior or set `llm.routing` to `auto`, `fast`, `strong`, or `pinned`. `pinned` keeps that agent on its assigned provider and model.

## Operational boundaries

Every candidate still passes the existing workspace and agent provider/model allowlists, region restrictions, model capability checks, token admission, cost governance, and cumulative run limits. Routing creates no token or spending limit of its own. A `0` runtime token or call limit continues to mean unlimited.

Soulacy switches candidates only for failures that are safe to retry elsewhere, including network failures, rate limits, provider overload, unavailable providers, quota exhaustion, invalid provider credentials, a missing model, a bad endpoint, or a context window that is too small. It does not retry policy denials, budget denials, cancellation, schema/application errors, or an unknown error on another model. Once streamed output has opened, the router does not switch providers mid-answer.

Failures open a per-provider/model circuit after the configured threshold. The circuit cools down before admitting that candidate again. `max_failovers` bounds the number of switches for one logical LLM call and is capped at five.

Advanced Settings can define ordered route lists for `simple`, `standard`, and `complex`, followed by a shared fallback list:

```yaml
llm:
  routing:
    enabled: true
    default_mode: auto
    preset: balanced
    max_failovers: 2
    failure_threshold: 3
    cooldown: 30s
    routes:
      simple:
        - { provider: ollama, model: qwen3:8b }
      standard:
        - { provider: openai, model: gpt-5-mini }
      complex:
        - { provider: anthropic, model: claude-sonnet-4-6 }
    fallbacks:
      - { provider: openai, model: gpt-5 }
```

Configuration updates are validated and applied to the running gateway immediately. An invalid provider, duplicate candidate, unsupported mode, or out-of-range circuit setting is rejected before the configuration file is changed.

## What each run records

The durable run ledger records a logical route ID and these events:

- `llm.route.decision` records the classification, ordered compatible candidates, rejected candidates, and selected provider/model.
- `llm.route.fallback` records the failed candidate, replacement, failure category, provider request IDs, latency, and token usage when available.
- `llm.route.completed` records all attempts and the final outcome.
- `llm.prompt` records request metadata, tool names, response format, character counts, estimated tokens, and a SHA-256 content hash.
- `llm.completion` records call latency, outcome, provider request IDs, token counts, and the initial response metadata.
- `llm.response.content` records the final assembled streamed response hash and usage.

Prompt and response text is excluded by default. Operators can enable full-content capture in Advanced Settings or configuration when the debugging value outweighs the privacy and storage cost:

```yaml
observability:
  capture_llm_content: false
  max_content_bytes: 262144
```

Captured content is size-limited, marked when truncated, passed through the existing secret redactor, and governed by the action log's retention and rotation settings. The hash always covers the complete value, including when stored content is truncated.

Prometheus exposes routing decision, fallback, and circuit-open counters:

- `soulacy_llm_route_decisions_total`
- `soulacy_llm_route_fallbacks_total`
- `soulacy_llm_route_circuit_opens_total`

The Providers API also reports current routing health, including consecutive failures, circuit state, and observed latency. Activity renders routing choices and fallbacks in plain language so an operator can see which model ran and why a run moved.

