# Costs API

The costs API exposes prompt-free accounting, admission readiness, estimates,
chargeback, and provider reconciliation. All routes use the `/api/v1` base and
require a principal with metrics permission.

## Agent summaries

```http
GET /api/v1/costs?since=30d&agent_id=researcher
Authorization: Bearer <token>
```

`since` accepts a duration such as `24h`, a supported period such as `30d`, or
an accepted date. `agent_id` is optional. The response contains `by_agent`, the
resolved period, and `generated_at`.

For one object-authorized agent:

```http
GET /api/v1/costs/researcher?since=7d
Authorization: Bearer <token>
```

## Cost readiness

```http
GET /api/v1/costs/status
Authorization: Bearer <token>
```

The readiness response reports:

- pricing-rule coverage and unknown-priced calls;
- daily/monthly budgets and the active enforcement mode;
- recorded spend and in-flight reserved spend;
- accounting attribution and rejected calls;
- retry attempts and forecast monthly spend;
- provider reconciliation variance;
- actionable checks and next actions.

Treat less than 100% accounting attribution or unknown-priced calls under a
strict policy as an operational defect.

## Prompt-free estimate

```http
POST /api/v1/costs/estimate
Authorization: Bearer <token>
Content-Type: application/json

{
  "provider": "openai",
  "model": "gpt-4.1-mini",
  "input_tokens": 12000,
  "max_output_tokens": 2000
}
```

The response includes normalized provider/model, estimated tokens, USD and
integer micro-dollars, pricing status/version, and whether the configured
confirmation threshold applies. No prompt text is accepted or stored.

## Per-call usage

```http
GET /api/v1/costs/usage?since=24h&limit=200
Authorization: Bearer <token>
```

`limit` must be between 1 and 1000. Records are bounded and prompt-free. They
can include subject, workspace, agent, run, inference-call ID, source, trigger,
provider/model, provider request IDs, retry attempts, token dimensions,
pricing status, outcome, and integer micro-dollar cost.

## Chargeback

```http
GET /api/v1/costs/chargeback?since=30d&group_by=user,feature,provider,model
Authorization: Bearer <token>
```

`group_by` is a comma-separated set of supported dimensions. The response
returns grouped usage and cost without prompts or tool payloads.

## Provider reconciliation

List recent comparisons:

```http
GET /api/v1/costs/reconciliations?limit=100
Authorization: Bearer <token>
```

Record a provider total for a completed period:

```http
POST /api/v1/costs/reconcile
Authorization: Bearer <token>
Content-Type: application/json

{
  "provider": "openai",
  "period_start": "2026-08-01T00:00:00Z",
  "period_end": "2026-08-02T00:00:00Z",
  "actual_usd": 12.34,
  "source": "provider-export"
}
```

The write route requires metrics-write permission. Periods accept RFC 3339 or
`YYYY-MM-DD`, and the end must be after the start. Automated reconciliation can
be configured instead; see [LLM usage and cost controls](../LLM_COST_CONTROLS.md).
