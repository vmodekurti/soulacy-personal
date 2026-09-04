# Install UX — what's broken, what's good, what to fix

Triggered by a real-world failure on 2026-06-08: after a fresh install, `sy agent tier` errored with `no agent_dirs configured (check ~/.soulacy/config.yaml)` even though the gateway's own config IS resolved correctly. The user was right to flag it: a framework that requires the operator to know about config paths, hardcoded directories, or stale binaries on `$PATH` is not "out-of-the-box."

This doc audits what's broken, what's already good (so we don't accidentally regress it), and the contained subset that gets the install experience to where it should be.

## What's already good (don't break it)

- **`internal/config/workspace.go::ResolveWorkspace()`** — single source of truth. Handles three layouts in one resolver: `$SOULACY_WORKSPACE` env override → existing `~/.soulacy/soulspace/` → legacy flat `~/.soulacy/` → new install defaults to soulspace. The fact this resolver exists is the right architecture; the bug is that not every binary consults it.
- **`internal/config/config.go::Load`** applies workspace-derived defaults to viper at gateway boot. `agent_dirs`, `memory.dir`, `knowledge.db_path`, log paths — all pre-filled with whatever `ResolveWorkspace()` returns.
- **`internal/app/wire.go:83`** calls `config.ResolveWorkspace()` and uses `ws.Root` consistently — the gateway itself respects the workspace correctly.
- **Install layout already includes a starter agent** (`basic-chat`) and all the supporting subdirs (`audit/`, `data/`, `logs/`, `memory/`, `plugins/`, `registry/`, `secrets/`, `skills/`, `templates/`, `tools/`, `whatsapp-web/`, `gui/`, `config.yaml`). A fresh install isn't a desert — it has functional defaults.

## What's broken (concrete bugs)

The root cause everywhere: **the `sy` CLI does NOT call `config.Load()`**, so viper inside `sy` doesn't have the workspace-derived defaults the gateway has. Any `sy` subcommand that reads `viper.GetStringSlice("agent_dirs")` (or similar) gets an empty result on a fresh install and either errors out or falls back to a hardcoded path that doesn't exist under the soulspace layout.

Concrete sites found in the audit (all in `cmd/sy/`):

| File:line | Symptom | Severity |
|---|---|---|
| `agent_tier.go::loadAgentsFromDisk` | Errored "no agent_dirs configured" until fixed in this pass. **Fixed** (now uses `ResolveWorkspace()`). | High — user-visible |
| `doctor.go:144 checkAgentDirs` | Same `viper.GetStringSlice("agent_dirs")` pattern, would have flagged a healthy install as broken. **Fixed** in this pass. | High — diagnostic lies |
| `main.go:1098-1103 skillsDir` | Returns hardcoded `~/.soulacy/skills` regardless of layout. Skills install into the wrong place on soulspace layouts. | Medium |
| `main.go:734 installSkill` | Same hardcoded skills path. | Medium |
| `pull.go:40 defaultAgents` | `defaultAgents := filepath.Join(homeDir, ".soulacy", "agents")` — `sy pull` lands agents in the wrong directory on soulspace. | Medium |
| `setup.go:725` | Returns hardcoded `~/.soulacy` for the runtime dir. Setup wizard may emit wrong instructions. | Medium |
| `doctor.go:81` | Has the right pattern (tries `ResolveWorkspace()`, falls back to `~/.soulacy`). Good model for others. | — |

## What "out-of-the-box" should mean

For a brand-new user, this should be the entire experience:

```
brew install soulacy            # or curl … | sh
soulacy serve                   # binds 127.0.0.1:18789, GUI live
```

Open `http://127.0.0.1:18789`, paste the API key the gateway just generated, see the GUI with the starter agent (`basic-chat`) listed, chat with it. **Zero file edits. Zero environment variables. Zero hunting for paths.** Every `sy ...` subcommand also works without `--agent-dir` flags or config edits, because both binaries resolve paths through the same helper.

What's missing today, in priority order:

### Tier 1 — landed in this pass

- **`sy agent tier` uses `ResolveWorkspace()`.** Works on soulspace installs with no config edit.
- **`sy doctor` reports correctly on workspace-derived `agent_dirs`.** No more spurious "no agent_dirs configured" on healthy installs.
- **`cmd/sy/workspace_paths.go::syWorkspace()`** — shared helper added so future subcommands don't re-introduce the bug. Wraps `ResolveWorkspace()` with a non-failing legacy fallback, returns a typed `Paths` struct.
- **First-run config + API key bootstrap.** New `internal/config/firstrun.go::EnsureBootstrap` writes a default config.yaml on virgin install with a freshly-generated `sy_` API key, or patches in just the key when config exists but key is empty. `cmd/soulacy/main.go::printFirstRunBanner` shows the URL + key once. Operator never has to `vim` a config file before first launch.
- **`mcp-servers` in `Paths.Dirs()`.** EnsureDirs creates it on first run so `sy doctor` doesn't flag a healthy install.

### Tier 2 — audit found these were already fine (no work needed)

The original audit miscounted. On re-verification, these call sites ALREADY use `ResolveWorkspace()` with a legacy fallback:
- `cmd/sy/pull.go:41-43` — `defaultAgents = ws.Agents` when resolver succeeds.
- `cmd/sy/setup.go:722-725` — `return ws.Root` when resolver succeeds.
- `cmd/sy/main.go:1099-1103 workspaceSkills` — `return ws.Skills` when resolver succeeds.
- `cmd/sy/doctor.go:82-84` — same pattern for runtime dir.

The grep that flagged them matched the `~/.soulacy` legacy-fallback inside the `if err == nil { return ws.X }` block, missing that those are the safety nets, not the primary path. Nothing to do here.

### Tier 3 — bigger improvements (separate sessions)

- **`soulacy serve` auto-bootstraps a config.yaml on first run.** If `ResolveWorkspace().ConfigFile` doesn't exist, generate one with sane defaults: `127.0.0.1`, port 18789, auto-generated API key, `provider: ollama` if `ollama` is reachable. Print the API key once to stdout with the URL. Operators never have to handwrite a config file.
- **`sy` calls `config.Load()` on startup.** Move config bootstrap into a `PersistentPreRun` on the root cobra command. Every subcommand inherits a fully-loaded viper without needing per-command fallbacks. Removes the entire class of bugs the audit found.
- **`brew install soulacy` works.** There's already a `homebrew-tap/` folder in the repo — needs a tap published + formula points at the latest release. Closes the "where do I download this?" gap.
- **First-launch experience in the GUI.** When the user opens the GUI before any agent exists, instead of an empty list show a "Pick a starter template" screen (we already have templates implemented — just route the empty state through it).
- **API key UX in the GUI.** Generate one if missing AND print it in the gateway startup banner. Today operators have to grep `config.yaml` for it — that's a paper-cut.

### Tier 4 — polish that turns "works" into "feels professional"

- `make install` updates `/usr/local/bin/{sy,soulacy}` atomically and emits the version. Today it's a plain `cp` — no version check, no atomic rename, requires `sudo` when overwriting a root-owned previous install. Use `install -m 0755` via a `sudo`-prompted wrapper, OR install to `~/.local/bin` by default (no sudo needed).
- `sy` warns when there's a newer `bin/sy` in the cwd vs the one on `$PATH` so people don't unknowingly run a stale system binary while iterating on a checkout.
- `sy --version` and `soulacy --version` report build commit / date / Go version so support questions are answerable. The Makefile already wires `internal/config.Version` via ldflags — confirm both binaries surface it via a top-level `--version` flag.
- README quickstart matches what the binary actually does. Today the README assumes the dev tree; it should assume a brew/curl install.

## The pattern that prevents future regressions

Every `sy` subcommand that reads or writes anything under the workspace MUST go through `config.ResolveWorkspace()`. Hardcoded `~/.soulacy/...` paths are a code-review red flag. A pre-commit hook (or just a `grep` in CI) on:

```
grep -rn '"\.soulacy/[a-z]*"' cmd/ internal/
```

catches them before they ship. The only legitimate occurrence is inside `internal/config/workspace.go` itself, where the constants are defined.

## What's deferred from this pass

- Tier 2 items (pull, skill install, setup paths, syWorkspace helper) — contained, ~150 LoC total, worth a dedicated session.
- Tier 3 items — each is a half-day's work; the auto-bootstrap one is the highest-leverage because it removes "configure before first run" entirely.
- Tier 4 — incremental polish, ship one at a time.

Two things landed in this pass: the `sy agent tier` and `sy doctor` bugs are gone. Use `./bin/sy agent tier` after `go build -o bin/sy ./cmd/sy` to verify.
