# Agents API

## Chat with an agent

```
POST /api/v1/chat
Authorization: Bearer <token>
Content-Type: application/json
```

### Request

```json
{
  "agent_id": "assistant",
  "user_id": "user-abc",
  "text": "Summarise the latest AI news"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_id` | string | ✅ | Agent to invoke |
| `text` | string | ✅ | User message text |
| `user_id` | string | — | Stable user/session key. Defaults to `api-user`. |
| `username` | string | — | Display name. Defaults to `user_id`. |
| `overrides` | object | — | One-run playground/test overrides. Does not mutate `SOUL.yaml`. |

### One-run overrides

```json
{
  "agent_id": "assistant",
  "user_id": "lab",
  "text": "Answer deterministically.",
  "overrides": {
    "provider": "ollama",
    "model": "qwen2.5:72b",
    "temperature": 0,
    "max_tokens": 800,
    "max_turns": 4,
    "tool_choice": "none"
  }
}
```

### Response

```json
{
  "reply": "Here's a summary of the latest AI news...",
  "session_id": "http-...",
  "run_id": "0c5f...",
  "response_id": "7e91..."
}
```

Use the server-issued IDs to rate the response:

```http
POST /api/v1/chat/feedback
Content-Type: application/json

{"agent_id":"assistant","session_id":"http-...","run_id":"0c5f...","response_id":"7e91...","rating":1}
```

`rating` is `1` for helpful or `-1` for unhelpful. An optional `comment`
creates a pending procedural-learning proposal for operator review.

### Streaming

Use `/api/v1/chat/stream` to receive Server-Sent Events.

```bash
curl -N -X POST http://localhost:18789/api/v1/chat/stream \
  -H "Authorization: Bearer sk_..." \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"assistant","user_id":"u1","text":"Tell me a joke"}'
```

Events:

```
data: Why
data:  don't
data:  scientists
...
data: [DONE]
```

---

## List agents

```
GET /api/v1/agents
Authorization: Bearer <token>
```

### Response

```json
{
  "agents": [
    {
      "id": "assistant",
      "description": "A helpful general-purpose assistant",
      "llm": {
        "provider": "openai",
        "model": "gpt-4o-mini"
      },
      "channels": ["http", "telegram"],
      "builtins": ["web_search"]
    },
    {
      "id": "researcher",
      "description": "Deep research agent",
      "llm": {
        "provider": "openai",
        "model": "gpt-4o"
      },
      "channels": ["http"],
      "builtins": ["web_search"]
    }
  ]
}
```

---

## Get agent

```
GET /api/v1/agents/{agent_id}
Authorization: Bearer <token>
```

### Response

```json
{
  "id": "assistant",
  "description": "A helpful general-purpose assistant",
  "llm": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "max_tokens": 1024
  },
  "system_prompt": "You are a helpful assistant.",
  "channels": ["http", "telegram"],
  "builtins": ["web_search"]
}
```
