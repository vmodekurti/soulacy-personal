# Credentials API

The credential vault stores agent-scoped sensitive values encrypted at rest.
The API deliberately separates listing a key name from revealing its value.
Global credential scope is denied on these routes; always provide an agent ID.

All values sent to or returned by the vault API are base64-encoded bytes.

## Store or replace a credential

```http
POST /api/v1/credentials/{agentID}
Authorization: Bearer <token>
Content-Type: application/json
```

```json
{
  "key": "openai_api_key",
  "value": "c2stLi4u"
}
```

A successful write returns `204 No Content` and emits an audit event.

## List credential names

```http
GET /api/v1/credentials/{agentID}
Authorization: Bearer <token>
```

```json
{
  "keys": ["openai_api_key"]
}
```

This route does not decrypt or return values.

## Reveal a credential value

Revealing plaintext requires the credential-reveal RBAC action and an explicit
confirmation header. The operation is audited.

```http
GET /api/v1/credentials/{agentID}/{key}
Authorization: Bearer <token>
X-Soulacy-Confirm-Credential-Reveal: true
```

```json
{
  "value": "c2stLi4u"
}
```

Decode the value only in the trusted process that needs it. Avoid printing it
to a terminal, log, support bundle, or CI output.

## Delete a credential

```http
DELETE /api/v1/credentials/{agentID}/{key}
Authorization: Bearer <token>
```

A successful delete returns `204 No Content`.

## Rotate a credential

Versioned vault backends can rotate a key without accepting a new plaintext
value from the caller:

```http
POST /api/v1/credentials/{agentID}/{key}/rotate
Authorization: Bearer <token>
```

```json
{
  "agent_id": "weather-expert",
  "key": "openai_api_key",
  "new_version": 2
}
```

List retained versions with:

```http
GET /api/v1/credentials/{agentID}/{key}/versions
Authorization: Bearer <token>
```

Backends without versioning return `501 Not Implemented` for rotation and
version listing.

## Using credentials

Keep secrets out of `SOUL.yaml`. Select a configured provider by name and let
the runtime resolve its credential through the configured provider/vault path:

```yaml
id: my-agent
llm:
  provider: openai
  model: gpt-4.1-mini
```
