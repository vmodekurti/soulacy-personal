# Telemetry

Soulacy emits OpenTelemetry traces and metrics, and tracks LLM cost per agent, user, and org.

## Reference

```yaml
telemetry:
  enabled: true

  # OpenTelemetry trace export
  otel:
    exporter: otlp          # otlp | jaeger | stdout | none
    endpoint: http://localhost:4318   # OTLP HTTP endpoint
    service_name: soulacy
    sample_rate: 1.0        # 1.0 = 100%, 0.1 = 10%

  # Prometheus metrics
  metrics:
    enabled: true
    path: /metrics          # scrape endpoint

  # Cost tracking
  costs:
    # Cost records are stored in the database. Pricing is optional; when a
    # provider/model is not listed, tokens are still recorded and cost_usd is 0.
    pricing:
      openai/gpt-4.1-mini:
        input_per_mtok: 0.40
        output_per_mtok: 1.60
      omniroute/*:
        input_per_mtok: 0.25
        output_per_mtok: 0.75
```

## Traces

Every agent invocation produces an OTEL trace with spans for:

- HTTP request handling
- Auth middleware
- Agent engine dispatch
- LLM provider call (including model, token counts, latency)
- Tool calls (built-ins, MCP tools, Python tools, peer agents, and plugins)
- Channel adapter send

### Jaeger (local dev)

```bash
docker run -d \
  -p 16686:16686 \
  -p 4318:4318 \
  jaegertracing/all-in-one:latest
```

```yaml
telemetry:
  otel:
    exporter: otlp
    endpoint: http://localhost:4318
```

Open [http://localhost:16686](http://localhost:16686) to view traces.

### Stdout (debug)

```yaml
telemetry:
  otel:
    exporter: stdout
```

Prints trace JSON to the server log — useful for debugging without a collector.

## Metrics

Prometheus metrics are available at `/metrics`:

| Metric | Type | Description |
|--------|------|-------------|
| `soulacy_requests_total` | Counter | Total HTTP requests by method, path, status |
| `soulacy_agent_invocations_total` | Counter | Agent calls by agent ID and status |
| `soulacy_llm_tokens_total` | Counter | LLM tokens consumed by provider and model |
| `soulacy_llm_latency_seconds` | Histogram | LLM response latency |
| `soulacy_rate_limit_hits_total` | Counter | Rate limit rejections by user |

## Cost tracking

Soulacy records token usage for every LLM call. Estimated dollar cost is computed when you configure a pricing table under `costs.pricing`; otherwise `cost_usd` remains `0` so the system does not invent prices.

Pricing keys match in this order:

- `provider/model`
- `provider/*`
- `*/model`

Values are USD per 1 million tokens:

```yaml
costs:
  pricing:
    anthropic/claude-sonnet-5:
      input_per_mtok: 3.00
      output_per_mtok: 15.00
    ollama_cloud/*:
      input_per_mtok: 0.00
      output_per_mtok: 0.00
```

```bash
# View cost summary
curl http://localhost:1947/api/v1/costs \
  -H "Authorization: Bearer $SOULACY_API_KEY"

# Filter by agent
curl "http://localhost:1947/api/v1/costs?agent_id=assistant&since=7d" \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

See the [Costs API reference](../api/costs.md) for full details.

## Run reliability

The dashboard's Run Reliability panel is backed by the durable action log, not the rolling per-agent JSONL tail. This makes the summary useful for cron jobs, manual triggers, and chat runs even after the visible log file rotates.

```bash
curl "http://localhost:1947/api/v1/runs/ops-summary?window=24h" \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

The response includes total runs, successful/failed/incomplete run counts, failure rate, tool calls, recent failures, top failing agents, and repeated error signatures. When cost tracking is enabled, it also includes total tokens and estimated cost for the same window.

## Disabling telemetry

```yaml
telemetry:
  enabled: false
```
