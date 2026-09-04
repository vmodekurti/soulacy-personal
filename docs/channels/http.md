# HTTP API Channel

The HTTP channel is always active and exposes a REST API for direct integration with web apps, mobile apps, and other services.

## Chat endpoint

```
POST /api/v1/chat
Authorization: Bearer <token>
Content-Type: application/json
```

### Request

```json
{
  "agent_id": "assistant",
  "user_id": "user-123",
  "text": "What is the capital of France?"
}
```

### Response

```json
{
  "reply": "The capital of France is Paris."
}
```

---

## Streaming (SSE)

For real-time token streaming, add `Accept: text/event-stream`:

```bash
curl -N -X POST http://localhost:18789/api/v1/chat/stream \
  -H "Authorization: Bearer sy_your-key" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"assistant","user_id":"u1","text":"Tell me a short story"}'
```

Events:

```
data: Once
data:  upon
data:  a time
data: [DONE]
```

---

## Session management

Sessions persist conversation history. Use the same `session_id` across requests to maintain context:

```bash
# Turn 1
curl -X POST .../api/v1/chat \
  -d '{"agent_id":"assistant","user_id":"alice-session","text":"My name is Alice"}'

# Turn 2 — agent remembers the name
curl -X POST .../api/v1/chat \
  -d '{"agent_id":"assistant","user_id":"alice-session","text":"What is my name?"}'
# Response: "Your name is Alice."
```

Sessions are stored in the configured storage backend and preserved across server restarts.

---

## Authentication

The HTTP channel respects the full auth stack:

| Token type | Header |
|-----------|--------|
| Server API key | `Authorization: Bearer sy_...` |
| Managed API key | `Authorization: Bearer sk_...` |
| JWT | `Authorization: Bearer eyJ...` |

See [Auth configuration](../configuration/auth.md) for details.
