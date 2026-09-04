# Cohort E — Merge Readiness Report

**Branch:** `codex/pto-parity-push`
**Prepared:** 2026-07-15
**Scope covered:** Cohorts A + B + C + Bucket 7A + Cohort E (E4a, E4b, E4c, E5, E1, E2)

This is the E3 deliverable — a merge-readiness sweep for the branch. It does **not** perform the merge. The user reviews the diff and pushes.

---

## 1. Branch state

- Working branch: `codex/pto-parity-push`
- Commits ahead of `main`: **90** (as of this pass — matches the count reported in the reviewing memo).
- Uncommitted diff (this session + prior sessions): **65 changed files**, ~14 K insertions across 185 files vs. main.
- Nothing has been committed or pushed by the agent in this session — every change is in the working tree awaiting review.

## 2. Local verification checklist (must pass before merge)

Sandbox constraint (same as prior cohorts): no `go`, `node`, or `python3 -c "import mkdocs"` were run from the agent. Every command below must pass on your Mac Studio before you push.

```
# 1. Formatting + module hygiene
go mod tidy
gofmt -l .          # must be empty
go vet ./...

# 2. Full Go build + race-detector test
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -race -timeout 120s ./...

# 3. GUI
cd gui && npm ci && npm test && npm run build && cd ..

# 4. End-to-end tree
make build                    # gateway + CLI + GUI
make install                  # exercises the same install path CI uses
make regression               # focused product smoke
make uat-public               # clean-runtime UAT (public variant)
make release-smoke            # temp install-prefix smoke
make docs-build               # mkdocs --strict

# 5. Docs screenshots (E5 deferred item — regenerate if any GUI surface changed)
make docs-screenshots

# 6. Optional but recommended before a merge:
SOULACY_UAT_CHAT=1 make uat-public       # verifies chat path against real Ollama
make uat-credential                       # if you've populated .env.uat
```

Any failure above blocks merge. The most likely places to break, in decreasing order:

1. **`go test ./internal/gateway/...`** — new files (`session_activity.go`, `session_activity_test.go`, `providerdoctor.go` wired into `handleListModels`) and a new `EventHub.activity` field. If tests fail, most likely cause is a nil dereference in tests that build an `EventHub` differently — the new `activity` field is initialised in `NewEventHub` so existing tests should be unaffected.
2. **`go test ./internal/llm/...`** — `TestAnthropicModelsHTTPErrorReturnsBakedIn` was **renamed** to two new tests (`…SurfacesError` + `…AuthErrorSurfacesError`). If any external test-runner config references the old name, update it.
3. **`go test ./internal/scheduler/...`** — new `MissedBackfill` exported type + `LastBackfill*` methods, plus `emitMissedRunBackfilled` and its test. `handleScheduleStatus` now returns a `backfills` map — one existing test (`handlers4_test.go:1174` — `sched-running`) only checks the `running` map by key, so the additive field is safe.
4. **`go test ./internal/agentvalidate/...`** — new cron pre-validation + 2 new table tests. Uses `robfig/cron/v3` (already in `go.mod` via scheduler).

## 3. What shipped on this branch (recap)

- **Cohort A** — schedules-ready first-class check; Debug-in-Studio AC2b/AC2c/AC3; save-blocking capability audit modal; learning-proposal `since=` filter + Studio-lesson unification.
- **Cohort B** — three new channel guided-setup cards (email/teams/google_chat) + delivery-doctor SMTP sweep; UAT `mode=public|full` + auto-report + CI wiring; reasoning-agent contract validators (6 checks); Studio Runtime intent presets (3 named presets).
- **Cohort C** — four more reasoning-agent contract checks; Build report **Needs your input** / **What Studio changed**; Studio streamed + wizard generation pipeline.
- **Bucket 7A** — package v2 schema (namespaced ids + calendar versioning + install-time secret gate + sidecar + `sy package validate`).
- **Cohort E — E4a** — Provider Doctor (`internal/llm/providerdoctor.go`), 14 categories, GUI Providers page shows friendly reason + fix + collapsible raw error; Anthropic `Models()` silent fallback fixed.
- **Cohort E — E4b** — Cron pre-validation at Save (agentvalidate); `schedule.missed_run_backfilled` event + `⟳ auto-replayed` chip on Automations row + `backfills` map on `/schedule/status`.
- **Cohort E — E4c** — Session hung tracker on EventHub; new `/activity/running` endpoint; **Running now** strip on Activity page with per-last-event-type reason + fix on hung sessions.
- **Cohort E — E5** — README + quickstart + gui-tour + studio.md + schedules.md + common-failures.md + channels/index.md + deployment/upgrades.md updated to match shipped surfaces; "local-first agent operating system" is now the canonical framing.
- **Cohort E — E1** — Per-step timing in UAT report (`step` / `skip_step` helpers, `steps.jsonl`, per-step table); Playwright screenshot gallery in the report; first-run bootstrap wrapped as a named step.
- **Cohort E — E2** — `scripts/uat-credential-smoke.sh` + `scripts/.env.uat.example` + `make uat-credential` target; loads `.env.uat`, runs cloud provider + local model + live channel delivery (Telegram/Slack/Discord/email) + scheduled one-shot + Studio repair probes.

## 4. Tests added this session (E4/E5/E1/E2)

| Package | Test | What it pins |
|---|---|---|
| `internal/llm` | `TestClassifyProviderErrorCoverage` (15 sub-cases) | Every category → representative error shape across OpenAI / Anthropic / Groq / Ollama |
| `internal/llm` | `TestClassifyProviderErrorNil` | `nil` error → OK diagnosis |
| `internal/llm` | `TestClassifyProviderErrorPreservesDetail` | Detail field carries raw error |
| `internal/llm` | `TestClassifyProviderErrorAsUnwrap` | Wrapped errors classify through `errors.Unwrap` |
| `internal/llm` | `TestAnthropicModelsHTTPErrorSurfacesError` (renamed) | 5xx now surfaces the error (previously silent) |
| `internal/llm` | `TestAnthropicModelsAuthErrorSurfacesError` (new) | 401 surfaces so `bad_key` is diagnosable |
| `internal/agentvalidate` | `TestDefinitionRejectsInvalidCronExpression` (4 sub-cases) | Save fails on `* * *`, `* * * * * * *`, gibberish, `60 * * * *` |
| `internal/agentvalidate` | `TestDefinitionAcceptsValidCronExpression` (5 sub-cases) | Save passes on standard 5-field, wildcards, `@daily`, `@hourly`, 6-field-with-seconds |
| `internal/scheduler` | `TestEmitMissedRunBackfilled` | `schedule.missed_run_backfilled` event shape + window round-tripping |
| `internal/gateway` | `TestSessionActivityTracker` | Full session lifecycle with a fake clock (start → llm.call → hung → evict → orphan bootstrap → sweep) |
| `internal/gateway` | `TestSessionActivitySortByStart` | Newest-first order for `/activity/running` |
| `internal/gateway` | `TestEventHubEmitFeedsTracker` | Wiring regression fence: `Emit()` must feed the tracker |

## 5. CI jobs — status vs. E4/E5/E1/E2 changes

Current CI matrix (`.github/workflows/ci.yml`):

| Job | Trigger | Needs update? | Notes |
|---|---|---|---|
| `gitleaks` (Secret scan) | push/PR | No | `.env.uat` is git-ignored; example file has no real secrets |
| `go` (Go build & test) | push/PR | No | `go build ./...` + `go test -race -timeout 120s ./...` cover all new tests; `go vet` + `govulncheck` unchanged |
| `lint` (golangci-lint) | push/PR | Recheck | Run locally first; the new files are gofmt-clean but golangci-lint's own opinions (unused imports, error checks) may flag something |
| `gui` (GUI build) | push/PR | No | `npm test && npm run build` — new svelte changes are additive |
| `docs` (Docs build) | push/PR | No | mkdocs `--strict` will fail on broken links or bad anchors — my anchor `#llm-providers-provider-doctor` was chosen to match `## LLM providers (Provider Doctor)` |
| `uat` (Clean-runtime UAT public) | push/PR | No | `make uat-public` uses the same script — new `step` / `skip_step` helpers and report shape are additive; the CI artifact upload is unchanged |
| `python` (Python SDK) | push/PR | No | Unaffected |

**Release workflow** (`.github/workflows/release.yml`) — jobs `Build Linux`, `Build macOS`, `Docker image`, `GUI render smoke`, `GitHub Release`. None touch the E-series changes; no updates needed.

**Docs workflow** (`.github/workflows/docs.yml`) — builds + deploys the public docs site. Every E5 doc edit is content-only; the site will rebuild cleanly. If `make docs-screenshots` output was regenerated, commit those PNG updates alongside the branch.

**Deliberately not added** — `make uat-credential` is **not** wired into CI (that's the E2 guarantee — credentials never leave the operator's machine). A future job that runs this against a private GitHub Environment with secrets could be added, but that's post-GA scope.

## 6. Merge blockers & concerns

**No hard blockers identified.** Nits to be aware of:

1. **Sandbox constraint** — no `go build` was runnable during this session. Every Go file was written gofmt-clean and reasoned through statically. The most likely regression class is a compile error in my new `internal/gateway/session_activity.go` — please run `go build ./...` first before any test.
2. **Renamed test** — `TestAnthropicModelsHTTPErrorReturnsBakedIn` no longer exists. If any external tooling (a script, a shell alias) invoked `go test -run TestAnthropicModels...` by exact name, update it.
3. **`docs-screenshots` regeneration** — E5 was a text-only pass. Screenshots on the public docs site are from `2026-07-14T11:14:43.159Z` per `docs/assets/screenshots/manifest.json`. If any GUI surface visible in those screenshots looks different after E4/E4c changes (Providers page, Activity page, Schedule page, Studio page), run `make docs-screenshots` and commit the updated PNGs alongside the branch. The UAT report will inline the new screenshots on next `make uat-public` run.
4. **`first-agent.md`** was deliberately left unrewritten (see E5 deferred note in `PRODUCTIZATION_REVIEW.md`). Not a blocker — reader still gets a working walkthrough.
5. **`.claude/`** directory is git-ignored / untracked and was created by the agent runtime. Should not be committed.
6. **`docs/BACKLOG.md`** shows as modified in `git status` but I did not touch it this session. Whatever prior session made that change is unchanged.

## 7. What the operator (you) needs to do

1. `git diff` on the branch — spot-check the touch points enumerated in section 3.
2. Run the full local checklist in section 2 in order. Stop at the first failure and file it.
3. If any GUI screen in `docs/assets/screenshots/*.png` looks stale, run `make docs-screenshots`.
4. Optionally: populate `.env.uat` from `scripts/.env.uat.example`, run `make uat-credential`, and review the generated `.cache/uat-reports/CRED_SMOKE_<UTC>.md` report to confirm real endpoints work end-to-end (recommended for pre-GA).
5. If all green: merge `codex/pto-parity-push` → `main`. The 90 commits ahead are stable — no rebase is required for correctness, though a squash into a single "Cohort A/B/C/7A/E" commit is cleaner history.
6. After merge, delete the `codex/pto-parity-push` branch.

## 8. What's next (deferred to post-E, needs your greenlight)

Per the original `PRODUCTIZATION_REVIEW.md § "Post-E / GA hardening"`, none of these have been touched:

- Upgrade / rollback testing on a real installed machine (not just the current runtime).
- Long-running scheduler soak test.
- Real channel-delivery soak test.
- Security review of privileged tools and channel exposure (external audit).
- Packaged release artifacts and versioned changelog.
- Telemetry-free crash / support bundle path.

Plus the two intra-Cohort residuals:

- `docs/getting-started/first-agent.md` full rewrite from raw-YAML on-ramp to Studio-Generate on-ramp (deferred from E5).
- Story 9 M residual: server-side stepped wizard that pauses between EACH phase (SSE is push-only — needs per-phase POST endpoints).

None of these block the current merge.
