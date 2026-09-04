# Release Checklist — v1.0.0

Ordered runbook for cutting the first production tag. Every step has an
acceptance signal; do not proceed until you see it. If a step fails, stop
and file the diff — no "we'll fix it after tag."

## Pre-flight (do these on a Monday, tag Tuesday)

### 1. Branch state

```bash
git checkout main
git pull --ff-only origin main
git status                    # clean working tree
git log --oneline -5          # confirm the merge commit is at the tip
```

**Acceptance:** clean working tree, last commit is the PR #37 merge, no local diffs.

### 2. Required parity — 13 checks

```bash
make production-parity
```

**Acceptance:** "Production parity passed with 6 optional check(s) skipped."
Report at `tmp/production-parity/<STAMP>/report.md`. If anything red, stop
here — do not tag until green.

### 3. Opt-in parity — the six credential-backed checks

Populate `scripts/.env.uat` from `scripts/.env.uat.example` first:

```bash
cp scripts/.env.uat.example scripts/.env.uat
$EDITOR scripts/.env.uat      # fill in OPENAI_API_KEY, SOULACY_GOLDEN_TELEGRAM_*, etc.
```

Then run the full opt-in harness:

```bash
bash scripts/uat-parity-full.sh
```

**Acceptance:** all six opt-in checks pass or skip cleanly (a skip is fine when
you don't have credentials for that adapter; a fail is a blocker). Report at
`.cache/uat-reports/CRED_SMOKE_<UTC>.md`. If Playwright isn't installed
locally, that specific check will error — install via `npx playwright install`
or skip it explicitly by unsetting `SOULACY_PARITY_BROWSER_RENDER`.

### 4. Docs sanity

```bash
mkdocs build --strict          # docs strict build (already run in parity)
```

Manually skim:

- `README.md` hero — should say "The agent framework you can put in production without a security memo" (post-launch-strategy positioning).
- `docs/index.md` hero + "what Soulacy is NOT" strip present.
- `docs/OPENCLAW_PARITY.md` voice row updated to "not v1 scope".
- `docs/LAUNCH_STRATEGY.md` §9 decisions are the locked answers (not the open questions).

**Acceptance:** strict build clean, tagline consistent everywhere, no stale "MVP foundation present" wording for voice.

### 5. Screenshot currency

If you regenerated any GUI in the last week:

```bash
SOULACY_PARITY_DOCS_SCREENSHOTS=1 make docs-screenshots
git status docs/assets/screenshots/
```

Commit any changed screenshots before tagging.

**Acceptance:** `docs/assets/screenshots/manifest.json` fresh; no dirty screenshots in git.

### 6. Changelog + release notes

- `CHANGELOG.md` has a `## [1.0.0] - <today>` section (already added; verify the date matches tag day).
- `docs/RELEASE_NOTES_v1.0.0.md` is the operator-facing release note (paste into the GitHub release later).

**Acceptance:** both files reflect what's in `main` at tag time.

## Tag day

### 7. Version stamp

```bash
grep -rn "config.Version" internal/config/version.go     # confirm current version
```

If the version constant is still `0.9.x`-ish, bump it to `1.0.0` and commit that
alone (`chore(release): bump version to 1.0.0`) before tagging.

### 8. Dry-run the release workflow

The release workflow at `.github/workflows/release.yml` triggers on tags matching
`v*`. Before pushing the real tag, verify the workflow file references no stale
secrets and that all needed secrets are in the repo settings:

- `GHCR_TOKEN` (or the default `GITHUB_TOKEN` if using GH's container registry)
- `HOMEBREW_TAP_TOKEN` (for the Homebrew tap bump)
- `SIGSTORE` / `COSIGN` keys (if signing artifacts)
- `APPLE_ID_USERNAME` + `APPLE_ID_PASSWORD` + `APPLE_TEAM_ID` (for macOS notarization)

Missing secrets → the workflow fails on the step that needs them. Add them under
Settings → Secrets and variables → Actions before tagging.

**Acceptance:** all required secrets present in the repo settings.

### 9. Tag and push

```bash
git tag -a v1.0.0 -m "Soulacy v1.0.0 — first public release

Cohorts A–H, security stack (S1–S7), Studio Debug-in-Studio, Learning
loop, Package v2, launch strategy memo. See CHANGELOG.md for details."
git push origin v1.0.0
```

**Acceptance:** tag exists on GitHub; the release workflow starts within ~30 s.

### 10. Watch the release workflow

Open <https://github.com/vmodekurti/soulacy-personal/actions/workflows/release.yml> and
follow the job. It should:

1. Build Linux amd64 + arm64 binaries via `Dockerfile.release`.
2. Build macOS amd64 + arm64 binaries + notarize.
3. Build the multi-arch container image and push to `ghcr.io/vmodekurti/soulacy-personal`.
4. Generate SBOM (SPDX) alongside binaries.
5. Cosign-sign each artifact.
6. Compute checksums into `SHA256SUMS`.
7. Bump the Homebrew tap formula.
8. Create the GitHub release with the compiled artifacts.

**Acceptance:** green workflow run; release page shows binaries + SBOM + SHA256SUMS + signatures.

### 11. Post-tag smoke test

Immediately test the install path a real user would hit:

```bash
# On a fresh machine (or a docker container / VM):
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash
soulacy serve &
sy chat --agent system "Hello!"
```

**Acceptance:** installs cleanly, gateway boots, `sy chat` replies.

## Post-tag (within 24 hours)

### 12. Publish the GitHub release notes

Edit the release GitHub auto-created and paste `docs/RELEASE_NOTES_v1.0.0.md` in
as the body. Attach the SBOM + checksums as visible download links.

### 13. Announce (per Decision 2 in LAUNCH_STRATEGY.md §9)

- Post HN Show HN thread (title candidates in `docs/LAUNCH_STRATEGY.md` §7).
- Twitter thread (draft ready before tag day, send 30 min after HN post lands).
- Newsletter outreach: Simon Willison, DevOps'ish, LangChain community.

### 14. Watch and respond

- HN comments: reply substantively for the first 4 hours; that's the algorithm window.
- GitHub Issues: triage anything filed within 24 h to signal responsiveness.
- Twitter mentions: acknowledge the top 5 within 6 h.

## Rollback plan

If a critical bug surfaces within 24 h:

```bash
# Yank the release from Homebrew and GHCR:
git push origin :refs/tags/v1.0.0    # remove remote tag
# Delete the GitHub release manually via the UI.
# Fix the bug, cut v1.0.1 through the same flow.
```

Announce the yank on the HN thread (people respect this). Do NOT try to
soft-patch — cut a real v1.0.1.
