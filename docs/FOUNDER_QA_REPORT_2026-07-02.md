# Founder QA Report — 2026-07-02

## Scope

Ran the production dogfood loop against the local Soulacy workspace:

- Gateway startup and restart
- Provider persistence after restart
- Onboarding status
- Template install with cron and Telegram output
- Telegram scheduled-output test
- Weather Expert manual run
- Stock Screener manual run
- Schedule cleanup
- Regression pack

## Passed

### Gateway and Providers

- Gateway starts on `127.0.0.1:18789`.
- Credential vault loads and reapplies 6 secrets.
- Provider doctor passes for 5 providers:
  - `google`
  - `nvidia`
  - `ollama`
  - `ollama_cloud`
  - `openroute`
- MCP connects:
  - `deal_intel`
  - `letsfg`
  - `weather`

### Onboarding

- `/api/v1/onboarding/status` returned `complete: true`.
- Required setup steps reported ready:
  - provider
  - agent
  - channel
  - template

### Weather Expert

- `expert-weather-agent` answered a real weather query for Austin, TX.
- Output included decision guidance, risk windows, alerts/safety, confidence, and a chart block.
- CLI chat with `weather_expert` also worked.

### Stock Screener

- Direct REST chat with `stock-screener` completed a test scan successfully.
- After fixing the CLI timeout, `sy chat --agent stock-screener` also completed successfully.

### Template Install, Schedule, Restart, Telegram

- Installed a temporary scheduled briefing agent with:
  - cron: `0 7 * * *`
  - output channel: `telegram`
  - Telegram destination from configured default output
- Schedule registered for the next 7 AM run.
- Restarted the gateway.
- Confirmed the scheduled agent reloaded after restart.
- Sent scheduled-output test through Telegram successfully.
- Deleted the temporary QA agent afterward.
- Final schedule state returned to empty.

### Regression

`make regression` passed:

- Go smoke packages
- Go binaries
- GUI focused tests
- GUI production build

## Fixed During QA

### CLI Chat Timeout

Problem:

- `sy chat --agent stock-screener ...` reported `cannot reach gateway` after 30 seconds, even though the gateway was healthy and the REST endpoint eventually completed.

Fix:

- Added `apiCallWithTimeout`.
- `sy chat` now uses a 10-minute timeout.
- Timeout errors now say the request timed out instead of falsely saying the gateway is unreachable.

### Telegram Token Redaction

Problem:

- Telegram shutdown/getUpdates errors printed the Bot API URL with token material.

Fix:

- Telegram adapter now redacts the configured bot token from logged/wrapped errors.
- Added unit test to prevent regression.

### Template Default Provider/Model

Problem:

- A scheduled template installed successfully, but after restart it was disabled because the template hard-coded `ollama/llama3.3:70b`, which is not available on this machine.

Fix:

- Template instantiation now inherits the configured default provider and model.
- Added gateway test coverage.

## Remaining Findings

### WhatsApp Web Is Misconfigured

Current state:

- `whatsapp_web` is enabled.
- Required `args` is missing.
- It is mapped to `flight-deal-finder`, but that agent is not currently loaded.
- Adapter is not connected and repeatedly emits QR messages.

Recommendation:

- Either disable `whatsapp_web` until ready, or configure the sidecar args and install/load the intended `flight-deal-finder` agent.

### Deal Finder Agent Is Not Loaded

The `deal_intel` MCP server is connected, but there is no loaded `deal-finder` / `flight-deal-finder` agent in the current workspace.

Recommendation:

- Recreate the Deal Finder agent from Studio or add a reusable Deal Finder template.

### No Active Production Schedule

After QA cleanup, there are no active scheduled agents.

Recommendation:

- Recreate the desired 7 AM production schedule once the target agent is finalized.

### GUI Build Warnings Remain

The GUI builds successfully, but Svelte still reports existing warnings:

- Studio accessibility warnings on clickable non-button elements
- PlanView accessibility warnings
- Palette accessibility warnings
- Some unused CSS selectors

Recommendation:

- Clean these before release polish, but they are not currently blocking build or runtime.

## Current Runtime State After QA

- Gateway: running on `127.0.0.1:18789`
- Agents loaded: 5
- Active schedules: none
- Telegram: connected
- Providers: healthy
- Regression: passing
