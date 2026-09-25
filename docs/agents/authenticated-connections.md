# Authenticated Website Connections

Authenticated connections let an agent read a subscription website after its
owner signs in once. Soulacy stores the resulting browser session in the
encrypted local vault and replays it for interactive or scheduled runs.
Passwords are never collected or stored.

Use this feature only with websites and accounts you are authorized to access.
Respect the publisher's terms, access limits, and copyright restrictions.

## Access model

Each connection belongs to the Personal installation owner and can be used only
by explicitly granted agents. Selecting a connection
in an agent definition is not sufficient by itself: the metadata store keeps a
second explicit grant. Runtime checks owner, status, expiration, agent grant,
and approved domain before decrypting any session state.

## Capture a browser session without shell access

1. Open **Website Access → Add website sign-in**.
2. Enter the sign-in URL and optionally narrow the approved domains and agents.
3. If prompted, download the Soulacy Session Capture companion, unzip it, open
   `chrome://extensions`, enable **Developer mode**, and select **Load unpacked**.
4. Select **Open secure sign-in**. Chrome asks for access only to the approved
   website domain and opens its normal login page.
5. Complete the normal sign-in, including password-manager, MFA, CAPTCHA, or
   passkey steps. Return to Soulacy and select **Save signed-in session**.

The companion reads cookies, including HttpOnly cookies, and local storage only
inside the approved domain boundary. It sends that state directly to the
signed-in Soulacy page, which writes it through the authenticated API to the
encrypted vault. The extension does not read passwords and does not persist
captured session state locally.

The bundled bridge activates on `*.soulacy.io`, `localhost`, and `127.0.0.1`.
A deployment using another hostname must add that exact origin to the
companion's `content_scripts.matches` before installing it.

### Remote CLI fallback

The CLI can capture in an isolated local Chrome profile and upload the result
to a remote Soulacy gateway. This works when the gateway platform has no shell:

```bash
sy --gateway https://beta.soulacy.io connection capture https://hbr.org/login \
  --name "HBR subscription" \
  --domains hbr.org \
  --agents daily-research
```

The command captures only cookies and origin storage inside the approved domain
boundary, uploads that state over the authenticated API, and removes the
temporary profile. The opaque state is encrypted and is never returned by list
or get APIs.

## Grant it to an agent

In Studio, open an agent and select the connection under **Website sign-ins**.
The saved `SOUL.yaml` reference is:

```yaml
connections:
  - conn_0123456789abcdef
```

When a ready, granted connection is present, the engine offers the agent
`authenticated_fetch`. Its description exposes only the connection display
name, ID, and approved domains. Cookies and tokens never enter the model prompt,
tool arguments, action log, or result.

The tool performs a read-only HTTPS GET using a cookie jar. It rejects embedded
credentials, private-network destinations, unapproved domains, and redirects
that leave the approved domain. HTML is converted to readable text and scripts
and styles are discarded.

## Scheduled runs

Grant the connection to the scheduled agent. Scheduled and interactive runs use
the same Personal installation-owner boundary, so no browser session is exposed
to an agent that was not explicitly selected in Studio.

## Reauthentication and revocation

If the website returns HTTP 401 or 403, Soulacy marks the connection as
**expired** and returns an actionable reconnect message. In Website Access,
choose **Reconnect**, sign in again, and save the session. The connection ID and
agent grants remain unchanged.

Deleting or revoking a connection immediately prevents new leases. Removing it
from an agent removes that agent's grant without affecting other agents.

## Current boundaries

- Cookie-authenticated, server-rendered pages work through
  `authenticated_fetch` today.
- Origin local storage is captured for forward compatibility. Pages that
  require client-side JavaScript or local-storage token injection need an
  isolated browser executor rather than HTTP fetch.
- CAPTCHA, passkeys, and MFA may require a fresh interactive sign-in. Soulacy
  does not bypass them.
- Hosted deployments must persist the authenticated-connection metadata
  database and encrypted credential vault with the gateway data directory.

See [Browser Automation](browser-automation.md) for workflows that must render
JavaScript, navigate, or interact with a page.
