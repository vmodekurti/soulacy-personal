# Browser Automation

Soulacy's supported Docker image includes a gateway-managed Chromium runtime.
Genie can use a provider website on a fresh Railway or Docker deployment
without shell access and without installing an MCP server.

The managed path is deliberately smaller than a general browser scripting
API. Genie can:

1. Open one official provider domain in an isolated temporary profile.
2. Replay a granted Website Access session inside that profile.
3. Inspect visible page text and stable element references.
4. Click, fill, select, and press ordinary controls.
5. Stop before booking, buying, sending, cancelling, or submitting.
6. Show the provider, action, item, time, terms, and exact total in an approval.
7. Submit only after approval, then return the provider page as confirmation
   evidence.

Passwords, passcodes, cookies, browser storage, payment numbers, and security
codes are never returned to Genie. Password and payment fields cannot be filled
through the model-facing action tool. Users complete sign-in, passkeys, MFA,
CAPTCHA, and payment setup directly on the provider website through Website
Access.

Managed sessions expire after 20 minutes, have a bounded process count, and
remove their temporary profile when closed. A session is tied to Genie and the
installation owner. Navigation outside the approved provider domain closes the
session and returns the attempted route plus an official fallback. All browser
traffic passes through a per-session guarded proxy that resolves and pins public
addresses and rejects localhost, private networks, CGNAT, link-local ranges,
and cloud metadata endpoints.

## Optional route: a remote browser

Use a remote browser when you intentionally need custom Playwright MCP tools or
browser capacity outside the gateway. Nothing is installed on the gateway.

On the **MCP Servers** page, choose the **Browser remote (CDP)** template and
replace the endpoint with your browser service URL:

```yaml
mcp:
  servers:
    playwright-mcp:
      transport: stdio
      command: npx
      args: ["-y", "@playwright/mcp@latest", "--headless",
             "--cdp-endpoint", "wss://your-browser-endpoint"]
      keeps_processes: true
```

## Optional route: a custom local browser MCP server

The managed runtime already covers Genie's provider website actions. The steps
below apply only when an agent needs a separate, general-purpose browser MCP
server.

The supported Docker image already contains `/usr/bin/chromium` and its system
libraries. A host install can use Chrome or Chromium already present on the
machine. Point the optional MCP server at that executable so it does not need a
second browser download:

```yaml
mcp:
  servers:
    playwright-mcp:
      transport: stdio
      command: npx
      args: ["-y", "@playwright/mcp@latest", "--executable-path", "/usr/bin/chromium",
             "--headless", "--isolated", "--no-sandbox"]
      keeps_processes: true
```

## Why each of those flags

Every one of them was a failure first.

| Setting | Without it |
| --- | --- |
| `--executable-path /usr/bin/chromium` | the server may look for branded Chrome or download another browser |
| `--headless` | it tries to open a window; there is no display on a server |
| `--no-sandbox` | Chromium cannot sandbox itself in a container: *"No usable sandbox!"* |
| `keeps_processes: true` | Soulacy's per-call process janitor kills the browser between tool calls, and the next call returns `about:blank` |

Use the browser path reported on the **Browser Trace** page when the deployment
does not use the supported Docker image.

## When it goes wrong

The failures here rarely name the thing that is broken.

| What you see | What it means |
| --- | --- |
| `initialize: stdio transport closed before response` | the server exited at startup: usually Node is too old (Playwright needs 20+) |
| `Chromium distribution 'chrome' is not found` | the optional MCP server was not pointed at the installed browser executable |
| `No usable sandbox!` | `--no-sandbox` is missing |
| `libsoftokn3.so: cannot open shared object file` | the custom image does not include Chromium's runtime libraries |
| *"the browser had closed"* on every navigation | the custom browser or its runtime libraries are incomplete |
| `Target page, context or browser has been closed` on a fresh start | an orphaned browser from a previous run still holds Playwright's runtime directory |
| `EACCES … /tmp/pw-*/browser/…sock` | that directory is owned by another user: usually created by running the server as root while the gateway runs unprivileged |

The deployment report on the **MCP Servers** page tells you which route your
deployment can take before you pick one. See
[what your deployment can do](../configuration/deployment.md).

## The process janitor, and browsers

Soulacy kills child processes an MCP tool leaves behind after a call returns,
so scheduled agents do not slowly fill a machine with stale browser windows.

A browser server is the exception, which is what `keeps_processes: true` is
for: its child process is not a leak, it is the page you navigated to. Without
the flag the browser is killed a second after `browser_navigate` returns, and
the next call reports `about:blank` with nothing to say why.

The exemption ends when the server stops. Whatever it left running is reaped
then (on a restart, or when the server is removed) because nothing owns those
processes any more, and an orphaned browser holds the runtime directory its
replacement needs.

## Agent Allowlist for optional MCP servers

For a narrow browser-enabled agent, explicitly allow the browser server:

```yaml
mcp_servers: [browser]
```

Or allow individual browser tools after you inspect the connected tool names:

```yaml
mcp_tools:
  - mcp__browser__browser_navigate
  - mcp__browser__browser_click
  - mcp__browser__browser_snapshot
```

Avoid wildcard MCP access for public or shared agents.

## Per-Agent Domain Policy for optional MCP servers

Browser MCP tools are treated as network tools by Soulacy's policy engine. Add a
`policy:` block to the agent so navigation is limited to the sites that workflow
is supposed to touch:

```yaml
policy:
  enabled: true
  network: prompt       # use "allow" for fully unattended trusted domains
  allow_domains:
    - example.com
    - docs.example.com
  deny_domains:
    - accounts.google.com
    - checkout.stripe.com
```

With `allow_domains` set, any browser navigation or MCP network call outside the
list is denied before the sidecar runs. With `network: prompt`, allowed domains
still require approval in interactive surfaces. For cron agents, prefer
`network: allow` plus a narrow `allow_domains` list so scheduled runs do not get
stuck waiting for a human.

## Trace And Artifacts

Every managed website action and browser MCP tool call is captured in the
action log. Open **Browser** in
the sidebar to replay an agent's browser steps by agent and optional session:

- navigate/click/type/extract/screenshot steps
- failed browser tool calls
- last navigated URL
- screenshot/file references when the sidecar reports them

This trace is read-only and best-effort. It is designed for debugging and audit:
when a browser workflow fails, use **Activity** for the full run and **Browser**
for the page-level sequence that led to it.

## Safety Notes

Browser automation can click, type, and submit forms. Treat it as an active tool
surface. The managed runtime enforces isolated profiles, domain boundaries, and
approval for final actions. For optional MCP servers, prefer dedicated browser
profiles, domain-specific agents, and human approval for workflows that spend
money, send messages, or mutate records.
