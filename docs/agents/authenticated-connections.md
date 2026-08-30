# Authenticated Website Connections

Authenticated connections let an agent read a subscription website after a
member signs in once. Soulacy stores the resulting browser session in the
encrypted workspace vault and replays it for interactive or scheduled runs.
Passwords are never collected or stored.

This feature is intended for websites and accounts the member is authorized to
use. Respect the publisher's terms, access limits, and copyright restrictions.

## Scope model

**Private to me** is the default. The connection belongs to the signed-in
member, is invisible to other members, and can be used only by explicitly
granted agents running as that member. A cron run retains the identity of the
member who created the schedule, so the same grant continues to work without
turning the login into a workspace credential.

**Workspace** is for a dedicated service account owned by the organization.
Only workspace owners and administrators may create, replace, grant, or delete
these connections. Use workspace scope for unattended team automations whose
access must survive an individual member leaving the workspace.

In both scopes, selecting a connection in an agent definition is not sufficient
by itself. The connection metadata store maintains a second, explicit grant for
that agent. Runtime checks workspace, owner, status, expiration, agent grant,
and approved domain before it decrypts any session state.

## Capture a browser session

1. Open **Connected Apps → Authenticated Websites → Add website sign-in**.
2. Enter the sign-in URL and optionally narrow the approved domains and agents.
3. If prompted, download the Soulacy Session Capture companion, unzip it, then
   open `chrome://extensions`, enable **Developer mode**, and select
   **Load unpacked**. Managed Team/Scale deployments can push the same pinned
   companion to members through Chrome enterprise policy.
4. Select **Open secure sign-in**. Chrome asks for access only to the approved
   website domain and opens its normal login page.
5. Complete the website's normal sign-in, including password-manager, MFA,
   CAPTCHA, or WebAuthn steps. Return to Soulacy and select
   **Save signed-in session**.

The companion reads cookies (including HttpOnly cookies) and local storage only
inside the approved domain boundary. It sends that state directly to the
signed-in Soulacy page, which writes it through the authenticated API to the
encrypted workspace vault. The extension does not read passwords and does not
persist captured session state locally.

The built-in bridge activates automatically on `*.soulacy.io`, `localhost`,
and `127.0.0.1`. Administrators using a custom Soulacy hostname must add that
exact origin to the companion's `content_scripts.matches` before distributing
the managed extension. Broad unrelated-site bridge permissions are not safe.

### Personal-mode CLI fallback

Personal mode also exposes the local CLI capture command for users who prefer
an isolated temporary Chrome profile:

Example:

```bash
sy --gateway https://team.example.com connection capture https://hbr.org/login \
  --name "HBR subscription" \
  --scope user \
  --domains hbr.org \
  --agents daily-research
```

The CLI fallback launches Chrome with a temporary profile, captures only cookies and
origin storage inside the approved domain boundary, uploads that state over the
authenticated API, and removes the profile. The API encrypts the opaque state
in the workspace credential vault; it is never returned by list or get APIs.

## Grant it to an agent

In Studio, open a reasoning agent and select the connection under
**Authenticated website connections**. The saved `SOUL.yaml` reference is:

```yaml
connections:
  - conn_0123456789abcdef
```

When at least one ready, granted connection is present, the engine offers the
agent `authenticated_fetch`. The tool description exposes only the connection
display name, ID, and approved domains. Cookies and tokens never enter the
model prompt, tool arguments, action log, or result.

The tool accepts an HTTPS URL and performs a read-only GET using a cookie jar.
It rejects embedded credentials, private-network destinations, unapproved
domains, and redirects that leave the approved domain. HTML is converted to
readable text and scripts/styles are discarded.

## Scheduled runs

For a personal subscription:

- create the connection with `--scope user`;
- grant it to the scheduled agent;
- create the schedule while signed in as the same member.

For a team-owned automation, use a dedicated publisher account and a workspace
connection. Do not share a personal publisher login by changing its scope.
Existing schedules created before creator identity was recorded must be saved
again before they can use a private connection.

## Reauthentication and revocation

If the upstream website returns HTTP 401 or 403, Soulacy marks the connection
as **expired** and fails with an actionable reconnect message. In Connected
Apps, choose **Reconnect**, complete the secure browser sign-in, and save the
session again. The connection ID and its agent grants remain unchanged.

Deleting or revoking a connection immediately prevents new leases. Removing it
from an agent also removes that agent's metadata grant without affecting grants
for other agents.

## Current boundaries

- Cookie-authenticated, server-rendered pages work through
  `authenticated_fetch` today.
- Origin local storage is captured for forward compatibility, but pages that
  require client-side JavaScript or local-storage token injection need the
  isolated browser executor integration rather than HTTP fetch.
- Generic OAuth refresh tokens require provider-specific token endpoint,
  client ID, and audience handling. Store them through a reviewed Connected App
  connector; do not treat a refresh token as an HTTP bearer token.
- CAPTCHA, WebAuthn, and MFA may require a fresh interactive sign-in. Soulacy
  does not bypass them.
- Team deployments must persist the authenticated-connection metadata database
  with the gateway data directory. Horizontally replicated Scale gateways need
  a shared metadata backend before enabling this feature across replicas.

See also [Browser Automation](browser-automation.md) for workflows that must
render JavaScript, navigate, or interact with a page.
