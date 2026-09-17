# Browser Automation

An agent can drive a real browser through an MCP server such as
[`@playwright/mcp`](https://github.com/microsoft/playwright-mcp): navigate,
read the page, click, fill forms. It is how an agent reaches things that have
no API.

Soulacy does not ship a browser. Most installs never drive one, and a browser
plus its libraries is several hundred megabytes that would otherwise sit in
every image. You choose one of two routes.

## Route 1: a remote browser (no local install)

Point the server at a browser running somewhere else. Nothing is installed on
your gateway — no Chromium, no system libraries, no download — so this works on
platforms where you have no shell.

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

## Route 2: a local browser

Two things are needed, and only one of them can be installed at runtime.

**The libraries.** Chromium links against system packages, and system packages
need root. Either build the image with them:

```bash
docker build --build-arg WITH_BROWSER_LIBS=1 -t soulacy .
```

…or install the bundle, which needs no root at all. The libraries do not have
to be *installed*, only *found*: unpacked into a directory on the library
search path they load from your volume exactly as well as from `/usr/lib`.

```bash
# Build the bundle (~13MB). It refuses to emit one that cannot browse.
scripts/build-browser-libs.sh --platform linux/amd64 --out dist

# Install it, with the checksum the script printed
curl -X POST http://localhost:18789/api/v1/browser/libs/install \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://…/browser-libs-amd64.tar.gz","sha256":"…"}'
```

The checksum is required, not optional: these files are loaded into the browser
process, so a bundle that is not the one you meant is code execution rather
than a corrupt download. The bundle lands on the mounted volume, so it survives
a redeploy. **Restart the gateway afterwards** — a server already running
inherited the old environment and cannot see the new directory.

**The browser itself.** Once the libraries are in place:

```bash
npx playwright install chromium
```

Point `PLAYWRIGHT_BROWSERS_PATH` at a directory on your volume (the supplied
`docker-compose.yml` already does) so it survives a redeploy too.

Then use the **Browser headless** template:

```yaml
mcp:
  servers:
    playwright-mcp:
      transport: stdio
      command: npx
      args: ["-y", "@playwright/mcp@latest", "--browser", "chromium",
             "--headless", "--isolated", "--no-sandbox"]
      keeps_processes: true
```

## Why each of those flags

Every one of them was a failure first.

| Setting | Without it |
| --- | --- |
| `--browser chromium` | the server looks for branded Chrome at `/opt/google/chrome/chrome` and exits |
| `--headless` | it tries to open a window; there is no display on a server |
| `--no-sandbox` | Chromium cannot sandbox itself in a container: *"No usable sandbox!"* |
| `keeps_processes: true` | Soulacy's per-call process janitor kills the browser between tool calls, and the next call returns `about:blank` |

`--browser chromium` also means Playwright resolves its own browser, so you do
not hard-code a path containing a build number that changes on the next
upgrade.

## When it goes wrong

The failures here rarely name the thing that is broken.

| What you see | What it means |
| --- | --- |
| `initialize: stdio transport closed before response` | the server exited at startup — usually Node is too old (Playwright needs 20+) |
| `Chromium distribution 'chrome' is not found` | `--browser chromium` is missing |
| `No usable sandbox!` | `--no-sandbox` is missing |
| `libsoftokn3.so: cannot open shared object file` | the library bundle is incomplete; rebuild it with the current script |
| *"the browser had closed"* on every navigation | the same thing as above — NSS aborts as soon as a page uses TLS |
| `Target page, context or browser has been closed` on a fresh start | an orphaned browser from a previous run still holds Playwright's runtime directory |
| `EACCES … /tmp/pw-*/browser/…sock` | that directory is owned by another user — usually created by running the server as root while the gateway runs unprivileged |

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
then — on a restart, or when the server is removed — because nothing owns those
processes any more, and an orphaned browser holds the runtime directory its
replacement needs.

## Agent Allowlist

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

## Per-Agent Domain Policy

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

Every browser MCP tool call is captured in the action log. Open **Browser** in
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
surface. Prefer dedicated browser profiles/accounts, domain-specific agents, and
human approval for workflows that spend money, send messages, or mutate records.
