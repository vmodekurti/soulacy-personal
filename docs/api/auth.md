# Auth API

Soulacy supports shared API-key authentication and short-lived JWT access
tokens. All protected requests use the standard bearer header:

```http
Authorization: Bearer <api-key-or-access-token>
```

## Exchange the server key for JWTs

This endpoint is available when `auth.mode: jwt` is enabled. It exchanges the
configured static server API key for an access/refresh pair.

```http
POST /api/v1/auth/token
Content-Type: application/json
```

```json
{
  "api_key": "sy_your-server-key"
}
```

```json
{
  "access_token": "eyJ...",
  "refresh_token": "opaque-refresh-token",
  "expires_in": 900,
  "token_type": "Bearer"
}
```

## Refresh an access token

Refresh tokens are single-use: a successful refresh rotates the refresh token
and returns a replacement alongside the new access token.

```http
POST /api/v1/auth/refresh
Content-Type: application/json
```

```json
{
  "refresh_token": "opaque-refresh-token"
}
```

## Inspect the current identity

```http
GET /api/v1/auth/me
Authorization: Bearer <token>
```

The response includes the authentication `mode`, subject and role. JWT
identities may also include `email`, `iat`, and `exp`.

## Create a managed API key

Managed keys use the `sk_` prefix. Creating and managing them requires config
administration permission.

```http
POST /api/v1/admin/api-keys
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "ci-bot",
  "scopes": ["read", "write"]
}
```

The `201 Created` response contains the key record and a plaintext `key`. Save
that value immediately; list operations never return it again.

## List managed API keys

```http
GET /api/v1/admin/api-keys
Authorization: Bearer <admin-token>
```

Pass `?include_revoked=true` to include revoked records.

## Revoke a managed API key

```http
DELETE /api/v1/admin/api-keys/{id}
Authorization: Bearer <admin-token>
```

Successful revocation returns:

```json
{
  "status": "revoked",
  "id": "<id>"
}
```

!!! warning "Do not expose validation endpoints"
    API-key management routes are administrative surfaces. Keep them behind
    Soulacy authentication and a trusted network boundary.
