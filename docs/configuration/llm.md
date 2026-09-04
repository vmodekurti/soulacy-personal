# LLM Providers

Soulacy supports multiple LLM providers simultaneously. Each agent picks its
provider and model with `llm.provider` and `llm.model` in `SOUL.yaml`.

## Reference

```yaml
llm:
  default_provider: openai   # used when model has no prefix
  timeout: 120s              # per-request LLM timeout

  providers:
    openai:
      api_key: "sk-..."
      base_url: https://api.openai.com/v1   # override for proxies

    anthropic:
      api_key: "sk-ant-..."

    ollama:
      base_url: http://localhost:11434

    together:
      api_key: "..."
      base_url: https://api.together.xyz/v1

    qwen:
      api_key: "..."
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
```

## Selecting a model in SOUL.yaml

Set the provider and model explicitly:

```yaml
llm:
  provider: openai
  model: gpt-4o-mini
```

For OpenAI-compatible routers such as OpenRouter, Together, Groq, vLLM, or LM
Studio, use the configured provider plus that service's model ID:

```yaml
llm:
  provider: openai
  model: moonshotai/kimi-k2
```

## Supported providers

| Provider | Config key | Notes |
|----------|-----------|-------|
| OpenAI | `openai` | GPT-4o, GPT-4o-mini, o1, o3 |
| Anthropic | `anthropic` | Claude 3.5, Claude 3 Haiku |
| Ollama | `ollama` | Local models, no API key needed |
| Together AI | `together` | Open-source models at scale |
| Qwen (Alibaba) | `qwen` | Qwen-Max, Qwen-Plus, Qwen-Turbo |

## Custom / compatible endpoints

Any OpenAI-compatible API (vLLM, LM Studio, Groq, etc.) works via the `base_url` override:

```yaml
llm:
  providers:
    openai:
      api_key: "not-needed"
      base_url: http://localhost:1234/v1   # LM Studio
```

## Timeouts

```yaml
llm:
  timeout: 180s   # increase for slow models or long context
```

Per-agent output caps are configured with `llm.max_tokens`; memory/context
injection is controlled by the agent's `memory` block. See the
[SOUL.yaml reference](../agents/soul-yaml.md).
