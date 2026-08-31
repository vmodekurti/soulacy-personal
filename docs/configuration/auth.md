# Authentication and sessions

Personal deployments can continue using one static API key. Team and Scale
deployments use short-lived Soulacy sessions backed by a standards-compliant
OpenID Connect provider. The GUI and `sy` CLI use the same identity and
workspace authorization model.

## Reference

```yaml
auth:
  mode: jwt
  jwt_secret: ${SOULACY_AUTH_JWT_SECRET}
  jwt_access_ttl: 15m
  jwt_refresh_ttl: 168h
  oidc_issuer: https://login.example.com
  oidc_client_id: soulacy
  # Optional reverse-proxy override. Otherwise Soulacy derives this from the
  # browser-facing origin, including the user-selected port.
  oidc_redirect_url: https://agents.example.com/api/v1/auth/oidc/callback
  oidc_scopes: [openid, profile, email]
```

Register the exact callback URL with the provider. For example, a Soulacy
instance opened at `http://localhost:1947` uses
`http://localhost:1947/api/v1/auth/oidc/callback`; choosing another port changes
that URL accordingly. `auth.oidc_redirect_url` is optional and should normally
be set only when a reverse proxy gives Soulacy a different public origin.
Providers that require a
confidential client secret can read `SOULACY_AUTH_OIDC_CLIENT_SECRET`; do not
commit the secret to `config.yaml`. Discovery must advertise authorization,
token and JWKS endpoints plus supported ID-token signing algorithms.

The fields above are also the deployment default used by the initial
bootstrap. Newly provisioned Team and Scale workspaces can activate their own
locked provider configuration through a one-time setup link. See
[Workspace identity and login](workspace-identity.md).

## Self-service SaaS signup

Hosted Team and Scale deployments can let a verified customer create their
first organization without giving them any deployment-level authority:

```yaml
signup:
  enabled: true

rate_limit:
  enabled: true
  per_user_rpm: 60
  backend: redis
  redis_url: rediss://redis.internal:6379
```

The global OIDC provider must return a verified email and the configured
scopes must include `email`. A new identity initially receives a short-lived
`onboarding` session that is accepted only by `GET/POST /api/v1/signup`; it
cannot pass workspace middleware. Tenant creation binds the owner to that
verified local user, allows one first organization per identity under a
database transaction lock, and ignores any client-supplied owner address.
After creation, normal session refresh resolves the new owner membership and
the browser continues to the expiring, one-time workspace identity setup link.

With strict billing enabled, the new owner can reach the billing remediation
routes while all ordinary mutations and agent runs remain blocked until Stripe
activates the workspace entitlement.

Self-service signup requires the shared Redis limiter. Soulacy uses one atomic
Lua operation for increment plus first-write expiry, refuses to start a
multi-user deployment when the configured Redis counter cannot connect, and
returns `503` if shared enforcement becomes unavailable at runtime. It never
silently falls back to a per-replica memory counter.

## Public demo workspace

A showcase deployment can admit any verified Google/OIDC user to one named
workspace without an invitation. This is intentionally different from signup:
it creates no organization and grants no permanent membership. See
[Public Demo Workspace](public-demo.md) for the constrained Studio role,
expiry, model/tool allowlists, and required quota configuration.

## Authentication flow

Requests are authenticated in this order:

1. **Static server API key** — `Authorization: Bearer sy_...`  
   Personal/bootstrap authentication only. Team and Scale reject it on every
   ordinary workspace API; use an interactive session, PAT, or service account.

2. **Managed API key** — `Authorization: Bearer sk_...`  
   Scoped keys stored in the database. Role is assigned at key creation time.

3. **Soulacy session JWT** — a short-lived token created after OIDC login.

External provider tokens are validated for issuer, audience, signature
algorithm, signature, and expiry. Interactive ID tokens additionally require
the one-time nonce. Local accounts are keyed by verified provider issuer and
subject; Soulacy never links an account from an unverified email address.

If none match, the request is rejected with `401 Unauthorized`.

## JWT

Issue a JWT by calling the token endpoint with your master API key:

```bash
curl -X POST http://localhost:1947/api/v1/auth/token \
  -H "Content-Type: application/json" \
  -d '{"api_key": "sy_your-server-key"}'
```

Access JWTs expire after 15 minutes by default. Refresh credentials rotate on
every use. Reusing an old refresh credential revokes the entire session family.
Configure a stable, strong signing secret:

```bash
openssl rand -hex 32
```

## CLI login

```bash
sy --gateway https://agents.example.com login
```

The CLI opens the provider in your browser and listens on a random
`127.0.0.1` callback. Tokens are stored in macOS Keychain or Linux Secret
Service, never in `config.yaml`. On a headless host, use the provider's device
authorization flow when it is advertised:

```bash
sy --gateway https://agents.example.com login --device --no-browser
sy --gateway https://agents.example.com logout
```

The CLI rotates an expired access session once and retries the original
request once. It never loops indefinitely on authentication failure.

## Managed API keys

Managed credentials (`sk_` prefix) are created via the credential-management
API. Secrets contain 256 random bits and only their SHA-256 digest is stored;
the plaintext is shown once. They carry an issuer, expiry, credential ID,
organization, explicit workspace bindings, role, and resource/action scopes.

```bash
# Create a 30-day service-account key. The service account and its workspace
# binding must already exist.
curl -X POST http://localhost:1947/api/v1/admin/api-keys \
  -H "Authorization: Bearer $SOULACY_ACCESS_TOKEN" \
  -H "X-Soulacy-Workspace: ws_production" \
  -H "Content-Type: application/json" \
  -d '{"name":"ci-bot","kind":"service_account","subject_id":"svc_ci","organization_id":"org_acme","workspace_ids":["ws_production"],"role":"operator","scopes":["agents:read","agents:write"],"expires_at":"2026-09-13T12:00:00Z"}'

# The response contains a plaintext `key` shown only once:
# {"id":"cred_abc123","key":"sk_xxx...","role":"operator",...}
```

Personal tokens always inherit the caller's subject and workspace role. Only
owners and admins can issue service-account credentials, and they cannot
delegate a role broader than their own. Rotation is atomic; suspension,
deletion, revocation, account suspension, and expiry take effect on the next
request without restarting Soulacy.

### Issuing credentials from the CLI

`sy credential` is the supported path for scripts and CI:

```bash
sy credential create ci-bot --kind service --subject svc_ci \
  --workspace ws_production --role operator \
  --scope agents:read,agents:run --expires-in 30d
sy credential rotate cred_abc123
sy credential revoke cred_abc123
```

CLI output and admin audit records both name the acting principal — a service
account appears as `service-account svc_ci`, never as a generic API user. The
audit entry for an issuance carries the credential ID, kind, subject,
organization, workspace bindings, role, scopes, and expiry, and never the
secret. See the [CLI reference](../cli/reference.md#scoped-credentials).

## RBAC roles

| Role | Permissions |
|------|------------|
| `owner` | Workspace lifecycle, membership audit, and all workspace operations |
| `admin` | Workspace operations and membership administration up to `admin` |
| `developer` | Build agents, skills, MCP integrations, knowledge, and templates without workspace administration |
| `operator` | Invoke agents, read costs, manage credentials |
| `viewer` | Read agents and metadata only |

In Team and Scale deployments, the role embedded in a login token is not the
workspace authorization source. Soulacy resolves the user's active membership
on every workspace request and uses the role stored in PostgreSQL. This makes
suspension, removal, and role changes effective on the next request without
waiting for a token to expire. See [Workspace members](workspace-members.md).
The complete resource/action matrix and object-grant rules are documented in
[Authorization policy](authorization.md).

## Credential vault

Sensitive credentials (LLM keys, channel tokens, third-party secrets) can be stored encrypted in the database and referenced by agents at runtime. See the [Credentials API](../api/credentials.md).
