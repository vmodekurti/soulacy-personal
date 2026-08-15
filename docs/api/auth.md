# Auth API

Soulacy supports shared API-key authentication and short-lived JWT access
tokens. All protected requests use the standard bearer header:

```http
Authorization: Bearer <api-key-or-access-token>
```

Browser sessions use the same short-lived tokens in `HttpOnly`, `SameSite`
cookies. Refresh cookies are limited to `/api/v1/auth`; no token is placed in
an OIDC callback URL or browser history.

## Interactive OIDC login

`POST /api/v1/auth/oidc/start` accepts `client: "cli"` with an exact loopback
`redirect_uri`, or `client: "gui"` using the operator-configured callback. It
returns the provider authorization URL containing a one-time state, nonce, and
S256 PKCE challenge. The CLI posts the returned code to
`POST /api/v1/auth/oidc/complete`; the GUI callback is
`GET /api/v1/auth/oidc/callback`.

Providers that advertise Device Authorization also enable
`POST /api/v1/auth/oidc/device/start` and `/device/poll`. Authentication errors
use one generic response and do not disclose whether an account exists.

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
and returns a replacement alongside the new access token. Reuse of a rotated
token revokes its entire token family.

```http
POST /api/v1/auth/refresh
Content-Type: application/json
```

```json
{
  "refresh_token": "opaque-refresh-token"
}
```

## Logout

`POST /api/v1/auth/logout` revokes the presented access token and refresh-token
family, clears browser cookies, and returns `204` regardless of prior session
state.

## Inspect the current identity

```http
GET /api/v1/auth/me
Authorization: Bearer <token>
```

The response includes the authentication `mode`, subject, role,
`principal_kind`, `organization_id`, workspace binding(s), `credential_id`,
issuer, and scopes. JWT identities may also include `email`, `iat`, and `exp`.

## Accept a workspace invitation

Invitation acceptance is authenticated but deliberately does not require an
existing workspace membership. Submit the one-time token while signed in with
the same verified email address to which the invitation was issued:

```http
POST /api/v1/invitations/accept
Authorization: Bearer <access-token>
Content-Type: application/json

{"token":"<one-time-invitation-token>"}
```

Acceptance is idempotent for the same user. Unknown, expired, reused by a
different user, and email-mismatched tokens all return the same error so the
endpoint cannot be used to enumerate invitations.

## Resolve the caller's workspace identity

```http
GET /api/v1/workspace/identity
Authorization: Bearer <token>
X-Soulacy-Workspace: ws_production
```

```json
{
  "subject": "usr_alice",
  "principal_kind": "user",
  "credential_id": "cred_abc123",
  "organization_id": "org_acme",
  "workspace_id": "ws_production",
  "membership_id": "mem_a1",
  "role": "developer",
  "scopes": ["agents:read"],
  "deployment_mode": "team"
}
```

`role` is the role resolved from the active stored membership, not the role
carried in the token. `scopes` is always an array. This is the schema behind
`sy whoami` and `sy context show`.

## List selectable workspaces

```http
GET /api/v1/workspace/workspaces
Authorization: Bearer <token>
```

```json
{
  "workspaces": [
    {"organization_id":"org_acme","organization_name":"Acme","workspace_id":"ws_production","workspace_name":"Production","membership_id":"mem_a1","role":"developer","principal_kind":"user"}
  ],
  "active_workspace_id": "ws_production"
}
```

The list is computed from stored memberships and service-account bindings on
every call, so a suspended or removed member stops seeing a workspace
immediately. A deployment that cannot enumerate workspaces returns `503` rather
than implying the active workspace is the only one.

## Select a workspace

```http
POST /api/v1/workspace/select
Authorization: Bearer <token>
Content-Type: application/json

{"workspace_id": "ws_production"}
```

Returns the verified membership. A workspace the caller may not act in returns
`404`, never `403`: confirming that a workspace exists but is closed to you is
an enumeration oracle.

## Create a managed API key

Managed keys use the `sk_` prefix. Creating and managing them requires
credential-management permission. In Team and Scale, the static server key
cannot call this or any other ordinary workspace endpoint.

```http
POST /api/v1/admin/api-keys
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "ci-bot",
  "kind": "service_account",
  "subject_id": "svc_ci",
  "organization_id": "org_acme",
  "workspace_ids": ["ws_production"],
  "role": "operator",
  "scopes": ["agents:read", "agents:write"],
  "expires_at": "2026-09-13T12:00:00Z"
}
```

The `201 Created` response contains the key record and a plaintext `key`. Save
that value immediately; list operations never return it again.

For `personal_access_token`, Soulacy ignores a supplied subject and role and
uses the authenticated caller. If expiry is omitted, the API assigns 90 days.
At least one action-aware scope is required. A service account and all of its
workspace bindings must exist and be active before its credential can be
issued.

## List managed API keys

```http
GET /api/v1/admin/api-keys
Authorization: Bearer <admin-token>
```

Pass `?include_revoked=true` to include revoked records.

Results are filtered to the active organization and workspace even for an
owner; credential IDs are not cross-tenant discovery handles.

## Rotate a managed credential

```http
POST /api/v1/admin/api-keys/{id}/rotate
Authorization: Bearer <owner-or-admin-token>
```

Rotation revokes the previous secret and creates its replacement in one
transaction. The response is the only time the new plaintext `key` is shown.

## Suspend or delete a managed credential

```http
PATCH /api/v1/admin/api-keys/{id}/status
Authorization: Bearer <owner-or-admin-token>
Content-Type: application/json

{"status":"suspended"}
```

Allowed states are `active`, `revoked`, `suspended`, and `deleted`. Any state
other than `active`, or an elapsed expiry, is rejected during authentication.

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
