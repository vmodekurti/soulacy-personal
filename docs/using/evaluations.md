# Evaluations (`sy eval`)

`sy eval` runs a structured test suite against a live agent and reports
pass/fail, so releases don't regress core behavior. Suites are plain JSON or
YAML, and a set of **golden suites** ships with Soulacy under `evals/golden/`.

## Running a suite

```bash
# a single suite (JSON or YAML)
sy eval --agent my-agent --suite evals/golden/weather.yaml

# every suite in a directory
sy eval --agent my-agent --suite evals/golden

# machine-readable output
sy eval --agent my-agent --suite evals/golden --json

# run only matching tags
sy eval --agent my-agent --suite evals/golden --tag weather

# repeat cases to catch flaky behavior and collect latency variance
sy eval --agent my-agent --suite evals/golden/weather.yaml --repeat 3

# stop as soon as the first failure appears
sy eval --agent my-agent --suite evals/golden --fail-fast
```

`sy eval` exits non-zero if any case fails, so it drops straight into CI.
The table report includes aggregate pass/fail/skip counts, latency
avg/p50/p95, and token totals when the gateway reports token usage.

## Writing cases

Each case sends an input to the agent and asserts on the reply:

```yaml
name: weather
cases:
  - name: current-conditions
    input: "What's the weather in London?"
    expected_contains: ["london"]           # all must appear (case-insensitive)
    expected_not_contains: ["as an ai"]      # none may appear
    expected_regex: ["(?i)(temperature|°)"]  # all must match
    expected_not_regex: ["(?i)error"]        # none may match
    max_tokens: 800                          # token budget
    max_latency_ms: 45000                    # latency budget
    tags: ["weather", "smoke"]               # selectable with --tag
    expect_tool_success: ["get_weather"]     # named tool ran and succeeded
    expect_delivered: true                   # channel delivery confirmed
    requires_secret: ["WEATHER_API_KEY"]     # skip (not fail) if unset
```

### Assertion types

| Field | Checks |
| --- | --- |
| `expected_contains` / `expected_not_contains` | Substrings in the reply |
| `expected_regex` / `expected_not_regex` | Regex matches on the reply |
| `max_tokens` | Reply token count is within budget |
| `max_latency_ms` | Response latency is within budget |
| `tags` | Case labels used by `sy eval --tag`; suite-level tags select every case in that suite |
| `expect_tool_success` | Each named tool was called and succeeded (asserted when the gateway returns a tool trace) |
| `expect_delivered` | The reported channel-delivery flag matches |

### Benchmark mode

Use `--repeat N` to run each selected case multiple times. This is useful for
spotting flaky agents, prompt drift, provider variance, and regressions where a
single pass hides a slow p95. Combine it with `max_latency_ms`, `max_tokens`,
and `--tag` for focused release checks:

```bash
sy eval --agent weather --suite evals/golden/weather.yaml --tag weather --repeat 5
```

### Secret-backed cases

A case that needs credentials lists them in `requires_secret`. If any named
environment variable is unset, the case is **skipped with a clear reason**
(`missing required secret(s): SLACK_BOT_TOKEN`) rather than failing. This lets CI
run the non-secret subset automatically while local runs execute the full set —
and the skip reason documents exactly what to set.

## Golden suites

The bundled golden suites cover the flagship surfaces:

`weather`, `stock-screener`, `deal-finder`, `research-librarian`,
`kb-ingestion`, `queues`, `studio-repair`, `telegram`, `slack`, `discord`,
`schedules`.

The channel suites (`telegram`, `slack`, `discord`) and live-data cases are
secret-backed and skip cleanly when their tokens aren't present.

## In CI

Run the non-secret subset on every push — cases needing secrets skip themselves,
so no configuration is required:

```bash
sy eval --agent ci-smoke --suite evals/golden --json
```

For the deterministic, offline Studio generation-robustness corpus (no gateway or
LLM needed), use:

```bash
sy eval generation
```
