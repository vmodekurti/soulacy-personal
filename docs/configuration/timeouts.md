# Request timeout hierarchy

Soulacy uses one strictly ordered deadline hierarchy for request processing:

`tool (2m) < LLM (3m) < step (4m) < run (15m) < HTTP (16m)`

Each outer deadline contains the work at the level to its left. The shipped
defaults are derived together and startup fails when an override inverts or
equalizes adjacent levels. Timeout errors identify the expired level and, when
available, the provider, model, or tool component.

```yaml
runtime:
  timeouts:
    tool: 2m
    llm: 3m
    step: 4m
    run: 15m
    http: 16m
```

`runtime.tool_timeout` remains a compatibility alias for `timeouts.tool`. When
both keys are present they must match. New configuration should use
`runtime.timeouts.tool`.
