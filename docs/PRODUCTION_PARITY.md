# Production Parity Automation

Soulacy ships a single production-readiness harness:

```bash
make production-parity
```

The harness runs the checks that should be green before a production release:

- Go vet, golangci-lint, full Go tests, and race tests.
- Full GUI unit tests and production GUI build through the regression smoke.
- Clean-runtime UAT against a disposable workspace.
- Release smoke using copied binaries from a temporary install path.
- `govulncheck` with a patched Go toolchain.
- Python SDK import.
- MkDocs strict build.
- Fresh `make install` and installed CLI version verification.

Every run writes:

- Markdown report: `.cache/production-parity/<timestamp>/report.md`
- JSON report: `.cache/production-parity/<timestamp>/report.json`
- One log file per check in the same directory.

If `.cache` exists as a file in the checkout, the harness automatically falls
back to `tmp/production-parity/<timestamp>/`.

## Optional Live Checks

Some parity checks require real credentials or external services. They are
reported as skipped unless explicitly enabled.

### Live Channel Certification

```bash
SOULACY_PARITY_LIVE_CHANNELS=1 \
SOULACY_API_KEY=... \
SOULACY_GOLDEN_TELEGRAM_TO=... \
SOULACY_GOLDEN_SLACK_TO=... \
SOULACY_GOLDEN_DISCORD_TO=... \
make production-parity
```

### Browser Sidecar Smoke

```bash
SOULACY_PARITY_BROWSER_MCP=1 make production-parity
```

### Browser Render Smoke

This launches a temporary Soulacy runtime, opens the main GUI routes in a
headless Chromium browser, fails on serious console/page errors, and saves
screenshots.

```bash
SOULACY_PARITY_BROWSER_RENDER=1 make production-parity
```

### Studio Build/Live UAT

```bash
SOULACY_PARITY_STUDIO_LIVE=1 make production-parity
```

## Toolchain Security

The vulnerability scan defaults to a patched Go toolchain:

```bash
SOULACY_PARITY_GOTOOLCHAIN=go1.26.6 make production-parity
```

Use a newer patched Go version when available.

## Python SDK Runtime

The SDK requires Python 3.10 or newer. Override discovery with:

```bash
SOULACY_PARITY_PYTHON=/path/to/python3.12 make production-parity
```
