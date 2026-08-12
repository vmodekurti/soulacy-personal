# Your First Agent

This guide walks through building and verifying a conversational `SOUL.yaml`
agent. If you prefer the GUI, [Studio](../using/studio.md) creates the same
definition, but you should still inspect the resulting YAML.

## Before writing YAML

Open **Providers**, configure the provider you intend to use, and select **Test
connection**. Provider and model names in an agent are references to the
gateway's registered catalog; Soulacy does not silently substitute an invented
provider when the reference is wrong.

## Minimal Agent

Every agent starts as a `SOUL.yaml` file:

```yaml
id: helper
name: Helper
description: A simple helper agent
trigger: channel
channels:
  - http
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are a helpful assistant.
enabled: true
```

Place this at
`~/.soulacy/soulspace/agents/helper/SOUL.yaml`. Soulacy's watcher reloads YAML
changes without a gateway restart in normal operation.

`trigger: channel` means an inbound message starts the agent. The `http`
channel includes Chat and API conversations. The final answer is returned to
the active conversation; a basic conversational agent does not need
`channel.send`.

## Add a Persona

Use `system_prompt` to shape behavior and scope:

```yaml
id: support-bot
name: Support Bot
description: Customer support for Acme Corp
trigger: channel
channels:
  - http
  - telegram
llm:
  provider: openai
  model: gpt-4o
system_prompt: |
  You are a friendly support agent for Acme Corp.
  - Only answer questions about Acme products.
  - If you do not know, say so and offer to escalate.
  - Keep replies under 150 words unless the user asks for detail.
  - Never reveal internal pricing or roadmap details.
enabled: true
```

## Add Built-ins

Built-ins are Go-native tools such as `web_search`, `kb_search`, and skill
readers. Use `builtins` to keep the catalog explicit:

```yaml
id: researcher
name: Researcher
description: Research assistant with current web search
trigger: channel
channels:
  - http
llm:
  provider: openai
  model: gpt-4o
system_prompt: |
  You are a research assistant. Search for current information and cite sources.
builtins:
  - web_search
enabled: true
```

See the [Agent Tools reference](../agents/tools.md) for the full tool model.

## Add a Python Tool

Agent-local tools are declared with a name, description, JSON Schema parameters,
and a `python_file`:

```yaml
tools:
  - name: summarize_file
    description: Summarize a local text file.
    python_file: tools/summarize_file.py
    timeout: 30s
    parameters:
      type: object
      properties:
        path: { type: string }
      required: [path]
```

Relative paths resolve from the agent directory.

## Add Memory

Memory controls how much stored context the agent may read and where it writes
new memories:

```yaml
memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000
```

Set `max_tokens: 0` or omit scopes for a leaner stateless agent.

## Multi-Channel Agent

Deploy the same agent to multiple channels:

```yaml
id: concierge
name: Concierge
description: Multi-channel concierge
trigger: channel
channels:
  - http
  - telegram
  - slack
  - discord
llm:
  provider: openai
  model: gpt-4o
system_prompt: |
  You are a concierge assistant. Be polite and efficient.
enabled: true
```

Each channel still needs credentials and routing in `config.yaml`.

## Choose the trigger deliberately

| YAML value | Starts when | Additional configuration |
| --- | --- | --- |
| `channel` | A bound channel receives a message | At least one item in `channels` and channel credentials/routing |
| `internal` | Chat, CLI/API, another agent, or an operator starts it | No cron required |
| `cron` | The scheduler fires | A `schedule` block and an outbound destination when delivery is required |
| `webhook` | The agent webhook endpoint receives an event | Webhook authentication and expected input shape |

Manual in Studio is saved as the internal/programmatic trigger. Do not add a
cron schedule merely because the agent may be run repeatedly.

## Full Example

```yaml title="agents/concierge/SOUL.yaml"
id: concierge
name: Concierge
description: Full-featured concierge agent
trigger: channel
channels:
  - http
  - telegram
  - slack

llm:
  provider: openai
  model: gpt-4o
  temperature: 0.2
  max_tokens: 1024

system_prompt: |
  You are a friendly concierge at Acme Inc.
  Help users with questions, research, and task management.
  Be concise. Use bullet points for lists.

builtins:
  - web_search

memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000

max_turns: 8
enabled: true
```

## Validate and exercise it

```bash
sy agent validate ~/.soulacy/soulspace/agents/concierge/SOUL.yaml
sy chat --agent concierge "What can you help me with?"
```

Then open **Activity** and verify the completed run used the expected provider,
model, tools, and channel. If validation reports an unknown provider, return to
**Providers** and use the registered provider ID rather than guessing.

## Add outbound delivery only when needed

Use `channel.send` for a scheduled notification, a one-off send, or delivery to
a destination other than the active conversation. It is a write action and is
commonly included in `confirm_tools`:

```yaml
builtins:
  - channel.send
confirm_tools:
  - channel.send
```

That configuration opens an approval dialog before sending. Do not add it to a
normal chat agent merely to display the final answer.

## Next Steps

- [SOUL.yaml Reference](../agents/soul-yaml.md) — complete schema documentation
- [Agent Tools](../agents/tools.md) — built-ins, Python tools, MCP, and peers
- [Workflow Steps](../agents/workflow.md) — checkpointed tool workflows
- [Configuration](../configuration/index.md) — server, LLM, and storage options
