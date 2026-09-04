# LLM usage and cost controls

Soulacy routes chat, reasoning, Studio, workflow, builder, synthesis, repair,
and embedding inference through one admission and accounting controller. The
controller reserves worst-case capacity before a provider request and records
actual or estimated usage when it finishes.

## Recommended production configuration

```yaml
llm:
  default_provider: openai
  allowed_providers: [openai]
  allowed_models: [gpt-4.1-mini]
  providers:
    openai:
      region: us
      retention: provider-managed
      local: false
      allowed_data_classes: [public, internal]
  studio:
    max_build_tokens: 100000
    max_build_cost_usd: 5

costs:
  enforcement_mode: hard
  unknown_pricing: block
  daily_budget_usd: 25
  monthly_budget_usd: 500
  per_user_daily_budget_usd: 2
  per_agent_daily_budget_usd: 10
  alert_threshold: 0.8
  default_max_output_tokens: 4096
  max_output_tokens_ceiling: 16384
  confirmation_threshold_usd: 0.25
  reservation_ttl: 15m
  max_concurrent_per_provider: 8
  circuit_failure_threshold: 5
  circuit_cooldown: 30s
  pricing:
    openai/gpt-4.1-mini:
      input_per_mtok: 0.40
      output_per_mtok: 1.60
      cached_input_per_mtok: 0.10
      source: https://provider.example/pricing
      effective_date: 2026-08-01
      version: august-2026

rate_limit:
  enabled: true
  per_user_rpm: 60
  per_user_tokens_day: 250000
  per_agent_tokens_day: 1000000
```

Use pricing from the provider's current official price sheet. Soulacy never
guesses the price of an unconfigured model. Such calls are recorded with
`pricing_status: unknown`; `unknown_pricing: block` rejects them before use.

## Enforcement modes

- `off` records usage and applies the output ceiling, provider concurrency,
  and circuit breaker, but does not enforce dollar or token budgets.
- `soft` also reserves estimated capacity and exposes threshold status without
  rejecting a call for budget exhaustion.
- `hard` atomically checks recorded plus in-flight usage, clamps the request's
  output allowance to remaining spend, and rejects calls that cannot fit.

Dollar periods are UTC calendar day and calendar month. User and agent token
quotas are rolling 24-hour windows. Scheduled, channel, HTTP, Studio, reasoning,
workflow, and embedding calls share the same controls.

Studio assigns a unique accounting run to every refine/generate/build request,
so concurrent builds cannot consume one another's per-build token or dollar
allowance. The dashboard confirms an above-threshold estimate before starting
a synchronous or streamed Studio operation.

## Accounting behavior

Each ledger entry can include the authenticated subject, workspace, agent,
session, run, inference-call ID, source, trigger, provider/model, provider
request ID, outcome, and normalized token dimensions. Cached input, cache
creation, reasoning, and tool-use prompt tokens are recorded when providers
report them. Integer micro-dollars are authoritative; `cost_usd` remains for
API compatibility.

HTTP retry attempts and every available provider request ID are retained on the
same idempotent inference-call row, so retries cannot double-count a logical
call. `GET /api/v1/costs/chargeback?group_by=user,feature,provider,model`
returns prompt-free grouped usage and cost totals for internal allocation.

Streaming calls are recorded after the stream closes. Native OpenAI usage
chunks and Ollama final counters are used when available; other compatible
streams receive a conservative tokenizer estimate, including cancelled or
partial streams.

## Model policy

Workspace `allowed_providers` and `allowed_models` apply to every governed
call. An agent can narrow them further with:

```yaml
llm:
  provider: openai
  model: gpt-4.1-mini
  allowed_providers: [openai]
  allowed_models: [gpt-4.1-mini]
  data_classification: internal
```

Playground overrides are evaluated after agent configuration, so an override
cannot escape either allowlist. The global output-token ceiling also applies to
overrides.

Only authenticated `admin` and `operator` roles may select a provider or model
override through interactive gateway surfaces. `POST /api/v1/costs/estimate`
returns a prompt-free estimate from caller-supplied token counts. When
`confirmation_threshold_usd` is non-zero, an interactive estimate above it is
rejected before provider access until the caller repeats the request with
`confirm_cost: true`.

Attach `data_classification` metadata to a run to enforce provider data-routing
policy. When a provider declares `allowed_data_classes`, missing classification
is treated as `unclassified` and must itself be explicitly allowed. Prompt and
tool contents are never stored in the cost ledger.

Workspace `llm.allowed_regions` can restrict routing to provider entries whose
`region` metadata is approved. Provider status surfaces `region`, `retention`,
`local`, and data-class policy without exposing credentials. Explicit prompt
caching is opt-in by classification through `cache_allowed_data_classes`; a
request outside that list has provider cache controls removed even when the
provider's `prompt_caching` switch is enabled.

## Operations

The cost-readiness endpoint reports calendar-period spend, in-flight reserved
spend, enforcement mode, and unknown-priced call count. An unknown-priced call
should be treated as an accounting coverage defect, not as a free call.

Every admitted or rejected router request receives a durable call ID. Cost
readiness reports accounting coverage, admission rejections, retry attempts,
pricing coverage, reservations, forecast, and reconciliation variance. A drop
below 100% accounting attribution is a failing readiness check and flows into
the existing operations alert evaluation.

Provider concurrency limits bound fan-out. Consecutive failures open a circuit
for the configured cooldown, preventing many agents from multiplying an
upstream outage into a retry and cost storm.

Set `llm.providers.<id>.max_tokens_per_minute` to the provider/project TPM
limit. Soulacy admits estimated input plus maximum output atomically against a
rolling minute and clamps when useful capacity remains. Provider `Retry-After`
values are capped at 30 seconds, while the request deadline remains the final
bound, so an upstream response cannot park workers indefinitely.

Provider invoices remain authoritative. Retain provider request IDs and compare
the ledger with provider usage/cost exports as part of billing operations.

Soulacy can perform that comparison automatically for the previous completed
UTC day:

```yaml
costs:
  reconciliation:
    enabled: true
    interval: 24h
    variance_alert_threshold: 0.10
    providers:
      openai:
        type: openai
        base_url: https://api.openai.com
        api_key_env: OPENAI_ADMIN_KEY
        organization: org_example
```

The admin key is read only from the named environment variable and is never
written to usage records, reconciliation reports, or logs. Re-imports are
idempotent. Variance above the configured threshold degrades cost readiness
and participates in the existing operator alert flow.

## Deployment verification

Before enabling unattended agents:

1. call `GET /api/v1/costs/status` and confirm accounting attribution is 100%;
2. ensure every permitted model has current pricing metadata;
3. verify recorded plus reserved spend is below the configured thresholds;
4. exercise `POST /api/v1/costs/estimate` with representative token counts;
5. confirm provider concurrency, TPM, retry, and circuit-breaker settings match
   the provider account limits;
6. compare the first full UTC day against the provider invoice or enable
   reconciliation.

Budget enforcement is a safety boundary, not a billing system of record.
Provider invoices remain authoritative.
