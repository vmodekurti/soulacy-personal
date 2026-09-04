# Rate Limiting

Soulacy enforces per-user and per-org rate limits to protect costs and prevent abuse.

## Reference

```yaml
rate_limit:
  enabled: true

  # Global defaults (apply to all users unless overridden)
  default:
    requests_per_minute: 60
    requests_per_hour: 500
    requests_per_day: 5000
    tokens_per_day: 500000      # LLM tokens consumed

  # Per-role overrides
  roles:
    admin:
      requests_per_minute: 0    # 0 = unlimited
    operator:
      requests_per_minute: 120
      tokens_per_day: 2000000
    viewer:
      requests_per_minute: 30
      tokens_per_day: 100000

  # Per-agent overrides
  agents:
    heavy-agent:
      requests_per_minute: 10

  # Storage backend for counters
  # Uses Redis if configured, otherwise in-process
  backend: redis                # redis | memory
```

## How it works

Rate limits use a **sliding window** algorithm. When a request arrives:

1. The user/org identity is extracted from the JWT or API key.
2. Current counts are fetched from Redis (or in-memory store).
3. If any limit is exceeded, `429 Too Many Requests` is returned with a `Retry-After` header.
4. Otherwise, the request proceeds and counters are incremented.

## Response headers

Every API response includes current limit headers:

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 42
X-RateLimit-Reset: 1717000000
Retry-After: 15          ← only on 429 responses
```

## Token budget tracking

Soulacy tracks LLM token consumption per user and per agent. Token counts are reported by the LLM provider and stored in the cost records table.

Token limits work alongside request limits — a single request with a very large context can exhaust the token budget even if the request count limit has not been reached.

## Distributed rate limiting

When running multiple Soulacy replicas, configure Redis so all nodes share the same counters:

```yaml
storage:
  redis:
    addr: redis:6379

rate_limit:
  backend: redis
```

Without Redis, each replica maintains independent in-memory counters, which allows up to `N × limit` total requests across `N` replicas.

## Disabling rate limits

```yaml
rate_limit:
  enabled: false
```

Not recommended in production.
