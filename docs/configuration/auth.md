# Auth & Security

Soulacy has a layered authentication system: static bearer tokens, managed API keys, and JWTs — all checked in sequence by the auth middleware.

## Reference

```yaml
auth:
  # JWT signing secret (HS256)
  jwt_secret: "your-jwt-secret-min-32-chars"
  jwt_expiry: 24h

  # Master server key (same as server.api_key, checked first)
  # Configured under server.api_key

  # Managed API keys (sk_ prefix)
  api_keys:
    enabled: true
    # Keys are stored hashed in the database.
    # Create via POST /api/v1/admin/api-keys

  # RBAC roles
  roles:
    - name: admin
      permissions: ["*"]
    - name: operator
      permissions: ["agents:read", "agents:invoke", "costs:read"]
    - name: viewer
      permissions: ["agents:read"]
```

## Authentication flow

Requests are authenticated in this order:

1. **Static server API key** — `Authorization: Bearer sy_...`  
   Full admin access. Matches `server.api_key` exactly.

2. **Managed API key** — `Authorization: Bearer sk_...`  
   Scoped keys stored in the database. Role is assigned at key creation time.

3. **JWT** — `Authorization: Bearer eyJ...`  
   Short-lived tokens issued by `POST /api/v1/auth/token`. Carry user identity, email, and role.

If none match, the request is rejected with `401 Unauthorized`.

## JWT

Issue a JWT by calling the token endpoint with your master API key:

```bash
curl -X POST http://localhost:18789/api/v1/auth/token \
  -H "Content-Type: application/json" \
  -d '{"api_key": "sy_your-server-key"}'
```

JWTs expire after `jwt_expiry` (default 24h). Configure a strong secret:

```bash
openssl rand -hex 32
```

## Managed API keys

Managed API keys (`sk_` prefix) are created via the admin API and stored as bcrypt hashes — the plaintext is shown only once at creation time.

```bash
# Create a key with operator role
curl -X POST http://localhost:18789/api/v1/admin/api-keys \
  -H "Authorization: Bearer sy_your-server-key" \
  -H "Content-Type: application/json" \
  -d '{"name": "ci-bot", "role": "operator"}'

# Response
{
  "id": "ak_abc123",
  "key": "sk_xxxxxxxxxxxxxxxxxxxx",   ← shown only once
  "role": "operator"
}
```

## RBAC roles

| Role | Permissions |
|------|------------|
| `admin` | All operations |
| `operator` | Invoke agents, read costs, manage credentials |
| `viewer` | Read agents and metadata only |

Roles are assigned at key or token creation time and embedded in the JWT claims.

## Credential vault

Sensitive credentials (LLM keys, channel tokens, third-party secrets) can be stored encrypted in the database and referenced by agents at runtime. See the [Credentials API](../api/credentials.md).
