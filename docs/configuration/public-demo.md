# Public demo workspace

Soulacy can expose one Team or Scale workspace as an invitation-free product
demo. A visitor opens the normal workspace URL, signs in with the deployment's
Google or standards-based OIDC provider, and receives a short-lived
`demo_developer` membership when the provider returns a verified email.

The visitor gets a broad, read-rich product tour plus a tightly bounded place
to act:

- Dashboard presents the demo guardrails and routes into the product.
- Studio can create, compile, test, and reopen **private, expiring drafts**.
- Chat can invoke only platform-seeded showcase agents carrying the explicit
  `soulacy.public_demo: "true"` label. Those agents use only the configured
  provider, model, and read-only built-in tools.
- Deployed agents, templates, knowledge, delivery, automations, skills, MCP,
  connected apps, plugins, and providers are visible as read-only product
  surfaces where their tenant-safe APIs permit it.

Drafts are scoped to both workspace and OIDC subject, expire automatically,
and are not visible to other visitors. The role cannot publish or enable an
agent, create schedules, mutate channels, install MCP servers or skills, open
connected-app sessions, change providers or secrets, write knowledge, invite
members, or reach deployment administration. Direct tool execution, uploads,
shares, attachments, webhooks, durable runs, feedback writes, and privileged
agent capabilities are also denied at the API. Hiding controls in the GUI is
only an additional usability measure.

## Configuration

Create a dedicated workspace and use its immutable ID—not its display name or
slug—in deployment configuration:

```yaml
deployment:
  mode: team

auth:
  mode: jwt
  jwt_access_ttl: 15m
  oidc_issuer: https://accounts.google.com
  oidc_client_id: ${GOOGLE_OIDC_CLIENT_ID}
  oidc_scopes: [openid, profile, email]

public_demo:
  enabled: true
  workspace_id: wrk_demo_01
  membership_ttl: 24h
  draft_ttl: 24h
  max_active_members: 100
  allowed_providers: [nvidia]
  allowed_models: [meta/llama-3.3-70b-instruct]
  allowed_tools: [web_search, fetch_url, generate_chart]

rate_limit:
  enabled: true
  backend: redis
  redis_url: rediss://redis.internal:6379
  per_user_rpm: 20
  per_user_tokens_day: 50000

costs:
  enforcement_mode: hard
```

Configure one of the allowed providers in the demo workspace vault and choose
an allowed Studio/runtime model. Visitors exercise their drafts through
Studio's private test/run-live path and can chat with the curated showcase
agents installed by the AWS helper. A normal workspace agent is never made
public merely by being deployed: the runtime requires the explicit public-demo
label and validates its complete capability envelope before every invocation.
Soulacy clamps omitted Studio and generated-agent provider/model values to this
allowlist; an explicit unapproved value is rejected. If no allowlisted provider
is actually registered, generation fails closed rather than falling back to a
deployment provider.

Share the workspace route (`/w/<workspace-slug>`). Normal members and invited
users retain their stronger existing role; demo admission is only attempted
after ordinary membership and invitation resolution fail.

## Operational limits

- Use a dedicated workspace containing no sensitive data, connections, files,
  or privileged agents. Read-only catalog surfaces may be populated for the
  tour, but never place tenant secrets or customer content in this workspace.
- Showcase agents must retain `soulacy.public_demo: "true"`, an explicit
  built-in allowlist, an allowlisted provider/model, bounded turns/tokens/calls,
  and no MCP, plugin, shell, code, delegation, knowledge, connection, channel,
  schedule, webhook, or unattended capability. The gateway rechecks these
  conditions at invocation time.
- Keep the JWT access TTL short. Membership expiry is checked on refresh and
  workspace resolution; an already-issued access token can live until its
  normal access expiry (15 minutes in the example).
- Redis is mandatory so quotas remain authoritative across gateway replicas.
  Daily token quotas require `costs.enforcement_mode: hard`.
- `max_active_members` counts only unexpired demo memberships. Returning users
  renew their own admission without consuming another slot.
- Purge or retain telemetry according to the same privacy policy used for
  ordinary workspace activity. Draft expiry is not a substitute for audit-log
  retention.

Disable the feature by setting `public_demo.enabled: false`. This prevents new
or renewed demo admission. Existing memberships remain usable only until their
stored expiry (and already-issued access tokens until their shorter token
expiry); the records can remain afterward for audit history.
