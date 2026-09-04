# Clean Runtime UAT Report — 2026-07-02

## Scope

Validated Soulacy against a separate runtime workspace so the normal local instance was not mutated.

- Workspace: `/tmp/soulacy-uat-20260702-87928`
- Config: `/tmp/soulacy-uat-20260702-87928/config.yaml`
- Gateway: `http://127.0.0.1:18890`
- API key: `sy_uat_20260702`
- Provider: local `ollama` with default model `qwen3:32b`

## Result

Overall: **pass**.

The runtime location override works. Soulacy created data, logs, secrets, memory, agents, and schedule state under the alternate workspace. The normal workspace was not used for the UAT agents.

## Passed

- Gateway booted with `SOULACY_WORKSPACE=/tmp/soulacy-uat-20260702-87928` and `SOULACY_CONFIG_PATH=/tmp/soulacy-uat-20260702-87928/config.yaml`.
- `sy doctor check` passed with `13 ok / 1 warn / 0 fail` after agent creation. The only warning was expected: no remote provider keys because this UAT used local Ollama only.
- First-run onboarding correctly showed:
  - provider connected
  - agents created
  - templates available
  - delivery channel still pending, because the clean workspace intentionally had no Telegram/Slack/Discord credentials
- Template catalog and onboarding suggested templates now advertise the runtime default model, `ollama / qwen3:32b`, instead of stale embedded example metadata.
- Installed `basic-chat` as `uat-basic-chat`.
- Installed `scheduled-briefing` as `uat-scheduled-briefing` with cron `0 7 * * *`.
- Installed a fresh corrected `scheduled-briefing` as `uat-scheduled-briefing-v2` after improving the starter template.
- Generated agent files persisted under `/tmp/soulacy-uat-20260702-87928/agents`.
- `uat-basic-chat` answered through the clean gateway with the expected reply: `clean runtime ok`.
- Scheduler registered `uat-scheduled-briefing` and calculated next run as `2026-07-03T07:00:00-05:00`.
- Manual trigger endpoint for scheduled agents returned HTTP 200.
- Corrected scheduled briefing template returned an honest setup/readiness note when no sources were configured instead of inventing live facts.
- Restart persistence passed: after shutdown and restart, gateway loaded 3 agents, re-registered the cron agent, and returned the same schedule.
- GUI shell was reachable: `/` returned 200 and `/manifest.webmanifest` returned 200.
- Regression pack passed: `make regression`.

## Fixed During UAT

The clean install exposed a model-metadata mismatch:

- `/api/v1/templates` originally previewed embedded template models like `ollama / llama3.3:70b`.
- Instantiation correctly rewrote agents to the configured default `ollama / qwen3:32b`.
- `/api/v1/onboarding/status` also used the raw embedded metadata in `suggested_templates`.

Fix:

- Template list and onboarding suggested templates now normalize previews to the runtime default provider/model.
- Template instantiation uses the same helper.
- Added gateway tests for template list, onboarding suggestions, and template instantiation defaulting.

The clean run also exposed a product-quality issue in the `scheduled-briefing` template:

- The first version executed successfully but invented stale live news/date content even though no source tools were configured.
- The embedded template now explicitly forbids invented current facts, API errors, or source results.
- When no topics/tools/knowledge are configured, it produces a setup/readiness note with concrete next steps.
- A fresh install, `uat-scheduled-briefing-v2`, was triggered successfully and returned the expected readiness-style output.

## Verification Commands

```bash
SOULACY_WORKSPACE=/tmp/soulacy-uat-20260702-87928 \
SOULACY_CONFIG_PATH=/tmp/soulacy-uat-20260702-87928/config.yaml \
./bin/soulacy serve

SOULACY_WORKSPACE=/tmp/soulacy-uat-20260702-87928 \
./bin/sy --gateway http://127.0.0.1:18890 --api-key sy_uat_20260702 --json doctor check

SOULACY_WORKSPACE=/tmp/soulacy-uat-20260702-87928 \
./bin/sy --gateway http://127.0.0.1:18890 --api-key sy_uat_20260702 --json chat \
  --agent uat-basic-chat 'Reply with exactly: clean runtime ok'

make regression
```
