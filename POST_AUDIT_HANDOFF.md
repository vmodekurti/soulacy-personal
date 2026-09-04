# Post-Audit Implementation — Handoff

This branch (`post-audit-fixes`) implements the story backlog in `improvements.md`.
**33 of 36 stories are implemented in code/config/docs and verified** (`go build ./...`
and the touched test suites are green). The remaining 3 are operational tasks that
only you can safely perform (credential rotation, git-history rewrite + force-push,
GitHub branch-protection). This file gives you the exact commands.

> **Note on commits:** the work is in the working tree but **not yet committed** —
> a stale `.git/index.lock` (held outside this session) blocked git writes the whole
> time. Clear it first, then commit (commands below).

---

## 0. Clear the git lock, then commit

```bash
cd <repo>
rm -f .git/index.lock .git/HEAD.lock          # remove the stale locks
git status                                     # sanity check

# Commit in logical groups (or just `git add -A && git commit` for one big commit):
git add improvements.md AUDIT_REPORT.md ROADMAP.md POST_AUDIT_HANDOFF.md
git commit -m "docs: post-audit roadmap, backlog, and handoff"

git add .github/ .golangci.yml .gitleaks.toml
git commit -m "CI: golangci-lint, race, gitleaks, govulncheck, gui/python tests (CI-1..5)"

git add internal/ pkg/ cmd/ go.mod go.sum
git commit -m "post-audit code fixes: SEC-3/4/5/7, ARCH-1/2/3/4/5, PERF-1..5, TEST-1..4, DEP-1"

git add docs/ mkdocs.yml README.md CHANGELOG.md SECURITY.md CONTRIBUTING.md \
        config.yaml.example .env.example Dockerfile Dockerfile.release \
        docker-compose.yml install.sh scripts/ sdk/ .gitignore
git commit -m "docs/release hardening: DOC-1/2/3/4, REL-1, SDK-1, HYG-2"

git add -A   # picks up the deleted personal scripts (HYG-3)
git commit -m "HYG-3: remove personal convenience scripts from repo root"
```

Then verify locally before pushing:

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -race -timeout 120s ./...
```

---

## Remaining operational stories (you must do these)

### SEC-1 · Rotate every leaked credential — DO FIRST
Every secret committed in `config.dev.yaml` / `docs/SESSION_HANDOFF.md` must be
treated as compromised. Rotate/revoke at each provider, then place new keys only
in git-ignored files (`.env`, `config.yaml` — both now covered by `.gitignore`):

- Anthropic API key
- Groq API key
- NVIDIA API key
- Slack app token **and** bot token
- Telegram bot token
- AlphaVantage API key
- Rocket Money / auth0 cookies
- Gateway admin/API key — regenerate (`openssl rand -hex 32`)
- **Re-pair the WhatsApp session** (its keys were committed under `.soulacy/`)

Confirm each old key is revoked at the provider before moving on.

### SEC-2 · Purge committed secrets from git history (after SEC-1)
History rewrite — **destructive, rewrites all SHAs, requires force-push**:

```bash
pip install git-filter-repo
git filter-repo --invert-paths \
  --path .soulacy/ \
  --path SESSION_HANDOFF.md \
  --path docs/SESSION_HANDOFF.md \
  --path config.dev.yaml \
  --path run-story1-tests.command \
  --path run_memory_tests.sh \
  --path-glob 'gui/vite.config.js.timestamp-*.mjs'

# re-add the remote (filter-repo drops it), then force-push:
git remote add origin git@github.com:vmodekurti/soulacy-personal.git
git push --force --all
git push --force --tags
```

Tell any collaborators/forks to re-clone. Verify clean:

```bash
git log --all -- .soulacy/        # should be empty
git ls-files | grep -c '^\.soulacy/'   # should be 0
gitleaks detect --source . -v     # should be clean
```

### HYG-1 · Tag + branch protection (after CI is green)
```bash
git tag pre-audit-fixes <sha-before-these-fixes>   # e.g. the commit before fb9f4dc
git push origin pre-audit-fixes
```
Then on GitHub: **Settings → Branches → Add rule** for `main` → require the CI
status checks to pass before merging, and disallow direct pushes.

---

## What was implemented (33 stories)

**Milestone 0:** CI-1 golangci-lint, CI-2 race detector, CI-3 gitleaks, CI-4
govulncheck, CI-5 gui/python tests, TEST-1 builtin-tool characterization tests.

**Milestone 1:** SEC-3 shell_exec gated off by default + per-agent `capabilities:
[system]`, SEC-4 auth hard-fail on non-loopback empty key (+ `--allow-unauthenticated`),
SEC-5 env allowlist for tool subprocesses, SEC-6 honest sandbox docs, ARCH-1
logged data-path errors, DEP-1 dependency patch upgrades, HYG-2 hardened
`.gitignore`.

**Milestone 2:** ARCH-2 split `buildSystemTools` into per-domain files, ARCH-3
gateway `errJSON`/`errMsg` helper (~243 sites), PERF-1 session eviction (TTL +
max-count), PERF-2 history windowing, PERF-3 action-log rotation, PERF-4
streaming `Tail`, TEST-2 Postgres parity tests, TEST-3 MCP client tests, DOC-1
unified org/Go-version/installer identity, DOC-2 NATS/Qdrant marked experimental,
ARCH-4 decomposed `wire.go Run()` (1335→283 lines) with LIFO shutdown stack.

**Milestone 3:** ARCH-5 shared LLM translation layer, SEC-7 unsigned-install
friction (`--allow-unverified`), DOC-3 SECURITY/CONTRIBUTING/CHANGELOG +
license fix, PERF-5 ctx-aware backoff + /health goroutine guard, DOC-4 actionlog
declared authoritative (audit JSONL off by default), SDK-1 Python SDK honesty,
HYG-3 removed personal scripts, REL-1 release signing + image pinning + no
default Postgres password, TEST-4 test ergonomics.
