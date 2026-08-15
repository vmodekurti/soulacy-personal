// Package runtime implements the agent execution engine.
// The Loader watches agent directories and hot-reloads SOUL.yaml files
// whenever they change. No restart required to deploy or update an agent.
package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/agent"
)

// SystemAgentID is the reserved ID for Soulacy's built-in web-only system
// agent. It is seeded in memory and cannot be replaced by SOUL.yaml files.
const SystemAgentID = "system"

// builtinSourcePath is the sentinel SourcePath stored on built-in agent
// definitions so LoadAll's stale-file cleanup never removes them.
const builtinSourcePath = "__builtin__"

// PersonalWorkspaceID is the implicit workspace every Personal deployment
// runs in. It matches the tenancy bootstrap ID so a Personal installation's
// agents keep their existing on-disk location after this package became
// workspace-aware.
const PersonalWorkspaceID = "ws_personal"

// workspaceRootDir namespaces non-personal workspaces on disk. Personal
// agents stay at <dir>/<id>/SOUL.yaml exactly as before; every other
// workspace lives under <dir>/.workspaces/<workspace-id>/<id>/SOUL.yaml.
//
// The layout is what makes workspace ownership structural rather than
// advisory: an agent's workspace is derived from where its file sits, never
// from anything inside the file. A SOUL.yaml cannot declare itself into
// another tenant, so no hot-reload or watcher event can move it across the
// boundary.
const workspaceRootDir = ".workspaces"

// agentKey makes (workspace, agent) the identity. Agent IDs are human-chosen
// slugs and collide across tenants by design; keying on the ID alone let the
// last directory walked silently win.
type agentKey struct {
	workspace string
	id        string
}

// Loader discovers and hot-reloads agent definitions from disk.
type Loader struct {
	dirs   []string
	agents map[agentKey]*agent.Definition
	mu     sync.RWMutex
	log    *zap.Logger
}

// AgentVersion is one immutable SOUL.yaml snapshot captured before an agent is
// overwritten or deleted.
type AgentVersion struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`
	CreatedAt   time.Time `json:"created_at"`
	Bytes       int       `json:"bytes"`
	// Actor is the principal that caused this snapshot. It is empty for
	// snapshots captured before version metadata was recorded, and for writes
	// that reached the loader without a request identity (a filesystem edit,
	// for instance).
	Actor string `json:"actor,omitempty"`
}

// versionMetadata is the sidecar written next to each snapshot. Snapshot
// timestamps used to come from the file's mtime, which a backup restore or a
// `cp -p` silently rewrites; recording creation explicitly keeps the history
// honest, and carries the actor the filesystem never knew.
type versionMetadata struct {
	Actor       string    `json:"actor,omitempty"`
	WorkspaceID string    `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// NormalizeWorkspace maps an absent workspace to the implicit personal one so
// every legacy call site keeps working unchanged.
func NormalizeWorkspace(workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return PersonalWorkspaceID
	}
	return workspaceID
}

// ValidateWorkspaceID rejects IDs that cannot safely become a path segment,
// for the same reason ValidateAgentID exists: the ID is about to be joined
// into a filesystem path.
func ValidateWorkspaceID(workspaceID string) error {
	if workspaceID == "." || workspaceID == ".." {
		return fmt.Errorf("workspace ID %q is not a usable directory name", workspaceID)
	}
	if !agentIDRe.MatchString(workspaceID) {
		return fmt.Errorf("workspace ID %q is not allowed: use 1-64 characters of a-z, 0-9, '.', '_' or '-'", workspaceID)
	}
	return nil
}

// workspaceAgentRoot returns the directory that holds one workspace's agents.
func workspaceAgentRoot(dir, workspaceID string) string {
	if NormalizeWorkspace(workspaceID) == PersonalWorkspaceID {
		return dir
	}
	return filepath.Join(dir, workspaceRootDir, workspaceID)
}

// workspaceForPath derives the owning workspace from a file's location under
// a configured agent directory. Returning ok=false means the path is not a
// legitimate agent location and must be ignored rather than guessed at.
func workspaceForPath(dir, path string) (string, bool) {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 || parts[0] == ".." {
		// The path escapes the configured agent directory. Walk never produces
		// one, but classifying an outside path as "personal" would be a quiet
		// way for a symlinked or misconfigured root to inject an agent.
		return "", false
	}
	if parts[0] == workspaceRootDir {
		// .workspaces/<id>/... — a file directly in the namespace root belongs
		// to no workspace and must not be guessed into one.
		if len(parts) < 3 {
			return "", false
		}
		if ValidateWorkspaceID(parts[1]) != nil {
			return "", false
		}
		return parts[1], true
	}
	return PersonalWorkspaceID, true
}

// NewLoader creates a Loader that watches the given directories.
func NewLoader(dirs []string) *Loader {
	l := &Loader{
		dirs:   dirs,
		agents: make(map[agentKey]*agent.Definition),
		log:    zap.NewNop(),
	}
	l.seedBuiltins()
	return l
}

// SetLogger attaches a structured logger used for YAML parse warnings.
// Call once at boot before the first LoadAll.
func (l *Loader) SetLogger(log *zap.Logger) {
	l.log = log.Named("loader")
}

// seedBuiltins pre-populates the in-memory agent registry with the built-in
// agents that ship with Soulacy. These agents are always available — they
// require no SOUL.yaml on disk and survive hot-reload cycles unchanged.
//
// Currently seeded:
//   - "system" — master chat agent with full OS-level tool access.
func (l *Loader) seedBuiltins() {
	system := builtinSystemAgent()
	l.agents[agentKey{PersonalWorkspaceID, system.ID}] = system
}

// builtinSystemAgent returns the Definition for the always-on system agent.
// The system agent is the "master of all": it can run shell commands, execute
// scripts, install libraries, read and write files, and list directories.
// It is available immediately on first run — no config or SOUL.yaml needed.
func builtinSystemAgent() *agent.Definition {
	return &agent.Definition{
		ID:          SystemAgentID,
		Name:        "System",
		Description: "Master system agent — run shell commands, scripts, install libraries, read/write files, and more.",
		Trigger:     agent.TriggerChannel,
		Channels:    []string{"http"},
		Enabled:     true,
		StreamReply: true,
		// Tuned for hard, multi-step instructions: more tool-call iterations
		// (MaxTurns), a longer overall budget (RunTimeout), and larger per-call
		// output (MaxTokens) so complex plans and answers aren't truncated.
		MaxTurns:   40,
		RunTimeout: "30m",
		// LLM intentionally left empty so the engine falls back to the
		// configured default provider and model (llm.default_provider in
		// config.yaml). This means the system agent works out of the box
		// regardless of which LLM provider the user has set up.
		LLM: agent.LLMConfig{
			Temperature: 0.2,
			MaxTokens:   8192,
		},
		// Require user confirmation before running any potentially destructive
		// or irreversible built-in tool. The SSE stream emits a tool_confirm
		// event; the GUI shows an approve/deny dialog before proceeding.
		ConfirmTools: []string{"package_install", "shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"},
		SystemTools:  true,
		Memory: agent.MemoryPolicy{
			ReadScopes:  []string{"session"},
			WriteScopes: []string{"session"},
			// Larger session-memory budget so the agent retains earlier steps,
			// results, and decisions across a long multi-step task instead of
			// losing context mid-run.
			MaxTokens: 2000,
		},
		SystemPrompt: `You are the Soulacy system agent — a general-purpose autonomous assistant with full access to the host machine, the internet, and the Soulacy runtime itself. You can research, install, configure, and operate software end-to-end with minimal human involvement.

## Working method (especially for hard, multi-step tasks)
Hard instructions are solved by decomposition and verification, not by guessing. For any non-trivial request:
1. **Plan first.** Before acting, write a short numbered plan of the concrete steps you'll take and the tools each needs. Restate the goal and success criteria in one line so you don't drift.
2. **Gather facts.** Don't assume the environment. Use sys_info, list_dir, read_file, env_get, and fetch_url to learn the actual state before changing anything.
3. **Execute one step at a time.** Run a single tool call, read its full output (stdout/stderr/exit code), and decide the next step from what actually happened — never assume a step succeeded.
4. **Verify every step.** After each install/config/file change, run an explicit check (version, health endpoint, re-read the file) and confirm it did what you intended before moving on.
5. **Self-correct.** If a command fails, read the error, diagnose the cause, and try a different approach. Adjust the plan rather than repeating the same failing call. Keep going until the goal is met or you hit a genuine blocker.
6. **Track progress.** For long tasks, periodically restate which plan steps are done and what remains, so context isn't lost across many turns.
7. **Persist.** You have a large turn budget — don't stop after one or two tool calls on a hard task, and don't hand work back to the user that you can do yourself. Only stop early for genuinely ambiguous choices or actions requiring credentials/approval you don't have.
8. **Finish with a summary.** End with what you did, what changed, how you verified it, and any manual steps left for the user.

## Tools

### Internet & HTTP
- **fetch_url(url, max_bytes?)** — GET a URL and return the body as text. Bare GitHub repo URLs (https://github.com/user/repo) auto-redirect to the raw README. Use this first whenever you're given a link.
- **http_request(method, url, body?, content_type?, headers?)** — Full HTTP client: POST, PUT, PATCH, DELETE with a body and custom headers. Use for REST APIs, webhooks, and service configuration calls.
- **download_file(url, dest_path)** — Download any URL (including binaries, archives, images) directly to disk. Parent directories are created automatically.

### Shell & Scripts
- **package_install(source_url, kind?, allow_unverified?)** — Install a Soulacy Skill or MCP server from a URL through the hardened package installer. Always use this for URL-based Skill/MCP installs; do not construct shell commands.
- **shell_exec(command, working_dir?, timeout_seconds?)** — Run any shell command. Returns stdout, stderr, and exit code. Default timeout 60s, max 600s.
- **run_script(script_path, interpreter?, args?, working_dir?)** — Execute a script file. Interpreter inferred from extension: .py→python3, .sh→bash, .js→node, .rb→ruby.
- **install_library(package_name, manager?, version?, global?)** — Install packages via pip, npm, brew, or apt.

### File System
- **read_file(path, max_bytes?)** — Read a file (supports ~ and $VAR; up to 1 MB).
- **write_file(path, content, append?)** — Write or append to a file; creates parent directories.
- **list_dir(path, show_hidden?)** — List directory contents with name, type, and size.
- **find_files(path, name_pattern?, content_pattern?, max_results?)** — Recursively search for files. name_pattern is a glob (e.g. "*.yaml"), content_pattern is a regex matched against file contents.

### Environment & System
- **env_get(name?)** — Read one environment variable by name, or list all if name is omitted.
- **sys_info()** — Return OS, architecture, hostname, user, home directory, CWD, and PATH.

## How to approach tasks

**"Install a Skill or MCP server from this URL"**
1. Call package_install with the URL and kind="auto". Do not fetch, clone, edit config, or invent CLI commands first.
2. The platform will ask the operator to approve or deny the exact installation.
3. Report the installer's verified result and any missing environment variables.

**"Install and configure other software for me"**
1. fetch_url the project URL or docs link to read setup instructions.
2. install_library or shell_exec to install.
3. Verify the installation and report remaining configuration.

**"What's running / what's installed?"**
Use sys_info for environment context, shell_exec for process/package listings (ps aux, brew list, pip list, npm list -g, etc.), find_files to locate config files.

**"Call an API / set up a webhook"**
Use http_request with the correct method and body. Read API docs with fetch_url first if needed.

**"Download and extract something"**
download_file to grab the archive, then shell_exec to extract (tar xf, unzip, etc.).

## Soulacy config format (YAML)
MCP servers live under the mcp.servers key in ~/.soulacy/config.yaml:

  mcp:
    servers:
      my-server:
        transport: stdio        # or "http"
        command: node           # stdio: executable
        args: [/path/to/server.js, --stdio]
        env:
          MY_API_KEY: "value"
      another-server:
        transport: http
        url: http://localhost:3000/mcp
        headers:
          Authorization: "Bearer token"

When adding an MCP server: read the existing config, insert the new block under mcp.servers, and write it back. Then tell the user to restart the Soulacy gateway for the change to take effect.

## Guidelines
1. **Act, don't ask** — for well-specified requests, carry out all steps and report what you did. Ask only when genuinely ambiguous (e.g. which API key to use).
2. **Show your work** — display stdout/stderr/exit codes and file paths so the user can verify each step.
3. **State intent before destructive ops** — one sentence before deleting, overwriting, or modifying system files.
4. **Verify success** — after installs/config changes, run a quick check (e.g. node --version, curl localhost:PORT/health) and report the result.
5. **Stay concise** — lead with the outcome, add details only if useful.`,

		// SourcePath uses the builtin sentinel so LoadAll never prunes this agent.
		SourcePath: builtinSourcePath,
	}
}

// LoadAll scans all configured directories and loads every valid SOUL.yaml it finds.
// Call this at startup and after any file-system event.
func (l *Loader) LoadAll() []error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error
	found := map[agentKey]bool{}

	for _, dir := range l.dirs {
		// Walk the directory looking for *.yaml and *.yml files
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip inaccessible paths
			}
			if info.IsDir() {
				if info.Name() == ".agent-history" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}

			// The owning workspace comes from the path and only from the path.
			// This is the whole defence against a hot-reload loading an agent
			// into another tenant: a SOUL.yaml has no say in where it belongs,
			// so dropping a file into one workspace's directory cannot reach
			// another, however the file is authored.
			workspaceID, ok := workspaceForPath(dir, path)
			if !ok {
				return nil
			}

			def, err := l.parseFile(path)
			if err != nil {
				errs = append(errs, fmt.Errorf("load %s: %w", path, err))
				return nil
			}
			if def.ID == SystemAgentID {
				def.ID = SystemAgentID
				def.Enabled = true
				def.SystemTools = true
				def.Channels = []string{"http"}
				if len(def.ConfirmTools) == 0 {
					def.ConfirmTools = []string{"package_install", "shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"}
				} else if !containsExactString(def.ConfirmTools, "package_install") {
					def.ConfirmTools = append(def.ConfirmTools, "package_install")
				}
				def.SourcePath = path
				key := agentKey{workspaceID, SystemAgentID}
				l.agents[key] = def
				found[key] = true
				return nil
			}

			key := agentKey{workspaceID, def.ID}
			l.agents[key] = def
			found[key] = true
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("walk %s: %w", dir, err))
		}
	}

	// Remove agents whose files have been deleted.
	// Built-in agents (SourcePath == builtinSourcePath) are permanent — they
	// live only in memory and are never written to disk, so we skip them here.
	for key, def := range l.agents {
		if def.SourcePath == builtinSourcePath {
			continue // never prune built-ins
		}
		if !found[key] {
			if key.id == SystemAgentID && key.workspace == PersonalWorkspaceID {
				l.agents[key] = builtinSystemAgent()
			} else {
				delete(l.agents, key)
			}
		}
	}

	return errs
}

func (l *Loader) parseFile(path string) (*agent.Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Two-pass decode strategy:
	//   Pass 1 — strict (KnownFields): catches YAML typos like `triggar` vs
	//            `trigger`, which would otherwise silently be ignored and leave
	//            the agent misconfigured. We only warn — the agent still loads.
	//   Pass 2 — lenient (standard Unmarshal): actual decode used for the
	//            returned definition, unaffected by unknown-field errors.
	//
	// This way a typo in config produces a visible WARNING in the log without
	// breaking the agent or requiring the operator to fix it before reloading.
	strictDec := yaml.NewDecoder(bytes.NewReader(data))
	strictDec.KnownFields(true)
	var strictCheck agent.Definition
	if strictErr := strictDec.Decode(&strictCheck); strictErr != nil {
		l.log.Warn("SOUL.yaml has unrecognised fields (possible typo — agent still loaded)",
			zap.String("path", path),
			zap.Error(strictErr),
		)
	}

	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if def.ID == "" {
		return nil, fmt.Errorf("agent definition missing required field 'id'")
	}

	def.SourcePath = path
	return &def, nil
}

// IsBuiltin reports whether the agent with the given ID is a built-in seeded
// at startup rather than loaded from a SOUL.yaml file on disk. Built-ins are
// excluded from wildcard peer expansion so they don't appear as callable tools
// unless an agent explicitly names them by ID.
func (l *Loader) IsBuiltin(id string) bool {
	return l.IsBuiltinInWorkspace(PersonalWorkspaceID, id)
}

// IsBuiltinInWorkspace reports whether the agent is a built-in. Built-ins are
// platform-provided and visible in every workspace, so this consults the
// workspace's own entry first and falls back to the seeded platform copy.
func (l *Loader) IsBuiltinInWorkspace(workspaceID, id string) bool {
	if id == SystemAgentID {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if d, ok := l.agents[agentKey{NormalizeWorkspace(workspaceID), id}]; ok {
		return d.SourcePath == builtinSourcePath
	}
	d, ok := l.agents[agentKey{PersonalWorkspaceID, id}]
	return ok && d.SourcePath == builtinSourcePath
}

// Get returns the agent definition with the given ID, or nil if not found.
//
// Returns a deep clone of the stored Definition so a hot-reload mid-run
// cannot mutate the pointer the engine is holding. Slice and map fields
// each get their own backing storage. See agent.Definition.Clone().
func (l *Loader) Get(id string) *agent.Definition {
	return l.GetInWorkspace(PersonalWorkspaceID, id)
}

// GetInWorkspace resolves an agent inside one workspace. A workspace that does
// not own the agent gets nil — not another workspace's definition — so a
// guessed ID reveals nothing about whether it exists elsewhere.
//
// Platform built-ins are the deliberate exception: they are provided by the
// installation rather than by a tenant, so every workspace sees them.
func (l *Loader) GetInWorkspace(workspaceID, id string) *agent.Definition {
	workspaceID = NormalizeWorkspace(workspaceID)
	l.mu.RLock()
	defer l.mu.RUnlock()
	if d, ok := l.agents[agentKey{workspaceID, id}]; ok {
		return d.Clone()
	}
	if d, ok := l.agents[agentKey{PersonalWorkspaceID, id}]; ok && d.SourcePath == builtinSourcePath {
		return d.Clone()
	}
	return nil
}

// All returns a snapshot of all loaded agent definitions. Each definition is
// a deep clone (same rationale as Get — see agent.Definition.Clone()).
func (l *Loader) All() []*agent.Definition {
	return l.AllInWorkspace(PersonalWorkspaceID)
}

// AllInWorkspace returns the agents visible in one workspace: its own, plus
// the platform built-ins. It never returns another workspace's agents, so a
// listing cannot be used to enumerate the deployment.
func (l *Loader) AllInWorkspace(workspaceID string) []*agent.Definition {
	workspaceID = NormalizeWorkspace(workspaceID)
	l.mu.RLock()
	defer l.mu.RUnlock()
	defs := make([]*agent.Definition, 0, len(l.agents))
	seen := map[string]bool{}
	for key, d := range l.agents {
		if key.workspace != workspaceID {
			continue
		}
		seen[key.id] = true
		defs = append(defs, d.Clone())
	}
	if workspaceID != PersonalWorkspaceID {
		for key, d := range l.agents {
			if key.workspace != PersonalWorkspaceID || d.SourcePath != builtinSourcePath || seen[key.id] {
				continue
			}
			defs = append(defs, d.Clone())
		}
	}
	return defs
}

// AllAcrossWorkspaces returns every agent in every workspace.
//
// This deliberately crosses the tenant boundary and exists only for
// deployment-level aggregates — boot validation and readiness counters — where
// the question really is "what does this installation contain". It must never
// back a response that a tenant reads, because the result set is the whole
// deployment. Anything user-facing uses AllInWorkspace.
func (l *Loader) AllAcrossWorkspaces() []*agent.Definition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	defs := make([]*agent.Definition, 0, len(l.agents))
	for _, d := range l.agents {
		defs = append(defs, d.Clone())
	}
	return defs
}

// AllWorkspaces lists every workspace that currently owns at least one agent.
// Background work that must sweep all tenants (schedulers, boot validation)
// uses this instead of reaching into the map.
func (l *Loader) AllWorkspaces() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for key := range l.agents {
		if seen[key.workspace] {
			continue
		}
		seen[key.workspace] = true
		out = append(out, key.workspace)
	}
	sort.Strings(out)
	return out
}

// SetEnabledInMemory flips the Enabled flag on the in-memory definition WITHOUT
// rewriting SOUL.yaml on disk. Used by boot-time validation (Story 2) to quarantine
// an agent whose configured model is unavailable — a hot-reload of the file will
// restore whatever the file says, which is the intended behaviour (fix the file,
// save, and it comes back). Returns false if the agent ID is unknown.
func (l *Loader) SetEnabledInMemory(id string, enabled bool) bool {
	return l.SetEnabledInMemoryInWorkspace(PersonalWorkspaceID, id, enabled)
}

// SetEnabledInMemoryInWorkspace flips Enabled for one workspace's agent only.
func (l *Loader) SetEnabledInMemoryInWorkspace(workspaceID, id string, enabled bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.agents[agentKey{NormalizeWorkspace(workspaceID), id}]
	if !ok {
		return false
	}
	d.Enabled = enabled
	return true
}

// agentIDRe is the set of IDs that are safe to use as a directory name and as
// part of a tool name. Deliberately narrow: an ID is a slug, not a filename.
var agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateAgentID rejects IDs that cannot safely become a path segment.
//
// "." and ".." are excluded explicitly even though the pattern would admit them,
// because both match `[a-z0-9._-]` and both traverse.
func ValidateAgentID(id string) error {
	if id == "." || id == ".." {
		return fmt.Errorf("agent ID %q is not a usable directory name", id)
	}
	if !agentIDRe.MatchString(id) {
		return fmt.Errorf(
			"agent ID %q is not allowed: use 1-64 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit. "+
				"The ID becomes a folder name and part of every tool name for this agent", id)
	}
	return nil
}

// Upsert writes or overwrites an agent definition to disk and reloads it in memory.
// Used by the GUI and CLI to persist agent changes without touching the filesystem directly.
//
// Each agent lives in its own folder: <dir>/<id>/SOUL.yaml. Legacy flat-file
// agents (<dir>/<id>.yaml) are migrated to the folder layout on the next write.
func (l *Loader) Upsert(dir string, def *agent.Definition) error {
	return l.UpsertInWorkspace(PersonalWorkspaceID, dir, def, "")
}

// UpsertInWorkspace writes an agent into one workspace's own directory and
// records the acting principal in the version history.
//
// The workspace is resolved into a path here rather than accepted as data, so
// a caller cannot write into another tenant by supplying a crafted directory.
func (l *Loader) UpsertInWorkspace(workspaceID, dir string, def *agent.Definition, actor string) error {
	if def.ID == "" {
		return fmt.Errorf("agent ID is required")
	}
	workspaceID = NormalizeWorkspace(workspaceID)
	if err := ValidateWorkspaceID(workspaceID); err != nil {
		return err
	}
	// The ID becomes a path segment on the very next line, and the ways an ID
	// gets here are not all typed by a person: the package importer takes it from
	// an uploaded archive, and Studio derives peer agent IDs from a MODEL-authored
	// workflow draft. `id: "../../../../root/.ssh"` therefore wrote SOUL.yaml (and
	// any package files) outside the agent root. agentvalidate only Warned about
	// path separators, and a Warn does not make a report invalid, so the import
	// route's validity check passed. Refuse here, at the point where the ID
	// actually becomes a path — the one place every caller goes through.
	if err := ValidateAgentID(def.ID); err != nil {
		return err
	}
	if def.ID == SystemAgentID {
		def.Enabled = true
		def.SystemTools = true
		def.Channels = []string{"http"}
		if len(def.ConfirmTools) == 0 {
			def.ConfirmTools = []string{"package_install", "shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"}
		} else if !containsExactString(def.ConfirmTools, "package_install") {
			def.ConfirmTools = append(def.ConfirmTools, "package_install")
		}
	}

	oldPath := def.SourcePath // where this agent currently lives (empty for new agents/imports)
	if oldPath == "" {
		l.mu.RLock()
		if existing := l.agents[agentKey{workspaceID, def.ID}]; existing != nil {
			oldPath = existing.SourcePath
		}
		l.mu.RUnlock()
	}
	if oldPath != "" && oldPath != builtinSourcePath {
		if err := l.snapshotPath(workspaceID, dir, def.ID, oldPath, actor); err != nil {
			l.log.Warn("agent history snapshot failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}

	root := workspaceAgentRoot(dir, workspaceID)
	agentDir := filepath.Join(root, def.ID)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(agentDir, "SOUL.yaml")
	data, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	// Migrate: remove the previous file if it was stored elsewhere (e.g. a legacy
	// flat <id>.yaml). Guard against deleting the file we just wrote.
	if oldPath != "" && oldPath != path {
		if _, statErr := os.Stat(oldPath); statErr == nil {
			_ = os.Remove(oldPath)
		}
	}

	def.SourcePath = path
	l.mu.Lock()
	l.agents[agentKey{workspaceID, def.ID}] = def
	l.mu.Unlock()
	return nil
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Register adds a definition to the in-memory registry WITHOUT touching disk, so
// the engine can run it but it is never persisted. Used for ephemeral agents like
// a Studio "try this" run. Pair every Register with an Unregister (defer).
func (l *Loader) Register(def *agent.Definition) {
	l.RegisterInWorkspace(PersonalWorkspaceID, def)
}

// RegisterInWorkspace adds an ephemeral definition visible only to one
// workspace, so a Studio "try this" run in one tenant is not runnable from
// another that happens to guess the id.
func (l *Loader) RegisterInWorkspace(workspaceID string, def *agent.Definition) {
	if def == nil || def.ID == "" {
		return
	}
	l.mu.Lock()
	l.agents[agentKey{NormalizeWorkspace(workspaceID), def.ID}] = def
	l.mu.Unlock()
}

// Unregister drops an in-memory-only definition added via Register. It never
// touches disk, so it is safe even if a same-id persisted agent exists (callers
// must use a unique ephemeral id to avoid evicting a real agent).
func (l *Loader) Unregister(id string) {
	l.UnregisterInWorkspace(PersonalWorkspaceID, id)
}

// UnregisterInWorkspace drops an ephemeral definition from one workspace.
func (l *Loader) UnregisterInWorkspace(workspaceID, id string) {
	l.mu.Lock()
	delete(l.agents, agentKey{NormalizeWorkspace(workspaceID), id})
	l.mu.Unlock()
}

// Delete removes an agent definition from disk and memory.
func (l *Loader) Delete(id string) error {
	return l.DeleteInWorkspace(PersonalWorkspaceID, id, "")
}

// DeleteInWorkspace removes an agent from one workspace only. Deleting an ID
// the workspace does not own is a no-op rather than an error, which is both
// idempotent and silent about whether that ID exists somewhere else.
func (l *Loader) DeleteInWorkspace(workspaceID, id, actor string) error {
	workspaceID = NormalizeWorkspace(workspaceID)
	l.mu.Lock()
	defer l.mu.Unlock()

	if id == SystemAgentID {
		return fmt.Errorf("agent %q is a protected built-in and cannot be deleted", id)
	}

	def, ok := l.agents[agentKey{workspaceID, id}]
	if !ok {
		// Already absent (e.g. a stale GUI row or a phantom left by an id rename).
		// Delete is idempotent — deleting something that's gone is success.
		return nil
	}
	if def.SourcePath == builtinSourcePath {
		return fmt.Errorf("agent %q is a built-in and cannot be deleted", id)
	}
	if err := l.snapshotPath(workspaceID, "", id, def.SourcePath, actor); err != nil {
		l.log.Warn("agent history snapshot failed", zap.String("agent", id), zap.Error(err))
	}
	if err := os.Remove(def.SourcePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	// If the agent lived in its own folder (<dir>/<id>/SOUL.yaml), remove the
	// now-empty folder too. os.Remove only succeeds on an empty dir, so this is
	// safe; it's a no-op for legacy flat-file agents.
	parent := filepath.Dir(def.SourcePath)
	if filepath.Base(parent) == id {
		_ = os.Remove(parent)
	}
	delete(l.agents, agentKey{workspaceID, id})
	return nil
}

// AgentVersions returns snapshots for an agent, newest first.
func (l *Loader) AgentVersions(id string) ([]AgentVersion, error) {
	return l.AgentVersionsInWorkspace(PersonalWorkspaceID, id)
}

// AgentVersionsInWorkspace returns one workspace's snapshots for an agent.
// History roots are per workspace, so a caller cannot read another tenant's
// definitions by asking for an agent ID it does not own.
func (l *Loader) AgentVersionsInWorkspace(workspaceID, id string) ([]AgentVersion, error) {
	workspaceID = NormalizeWorkspace(workspaceID)
	var out []AgentVersion
	for _, root := range l.historyRoots(workspaceID, "") {
		dir := filepath.Join(root, id)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			version := AgentVersion{
				ID:          strings.TrimSuffix(entry.Name(), ".yaml"),
				AgentID:     id,
				WorkspaceID: workspaceID,
				Path:        path,
				CreatedAt:   info.ModTime().UTC(),
				Bytes:       int(info.Size()),
			}
			applyVersionMetadata(&version, path)
			out = append(out, version)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// ReadAgentVersion returns the raw SOUL.yaml captured in a version snapshot.
func (l *Loader) ReadAgentVersion(id, version string) ([]byte, AgentVersion, error) {
	return l.ReadAgentVersionInWorkspace(PersonalWorkspaceID, id, version)
}

// ReadAgentVersionInWorkspace reads a snapshot from one workspace's history.
func (l *Loader) ReadAgentVersionInWorkspace(workspaceID, id, version string) ([]byte, AgentVersion, error) {
	workspaceID = NormalizeWorkspace(workspaceID)
	version = filepath.Base(strings.TrimSpace(version))
	if version == "." || version == "" || strings.Contains(version, string(filepath.Separator)) {
		return nil, AgentVersion{}, fmt.Errorf("invalid version id")
	}
	if !strings.HasSuffix(version, ".yaml") {
		version += ".yaml"
	}
	for _, root := range l.historyRoots(workspaceID, "") {
		path := filepath.Join(root, id, version)
		data, err := os.ReadFile(path)
		if err == nil {
			info, _ := os.Stat(path)
			v := AgentVersion{ID: strings.TrimSuffix(filepath.Base(path), ".yaml"), AgentID: id, WorkspaceID: workspaceID, Path: path, Bytes: len(data)}
			if info != nil {
				v.CreatedAt = info.ModTime().UTC()
				v.Bytes = int(info.Size())
			}
			applyVersionMetadata(&v, path)
			return data, v, nil
		}
		if !os.IsNotExist(err) {
			return nil, AgentVersion{}, err
		}
	}
	return nil, AgentVersion{}, fmt.Errorf("agent version not found")
}

// RestoreAgentVersion rolls an agent back to a previous SOUL.yaml snapshot.
func (l *Loader) RestoreAgentVersion(dir, id, version string) (*agent.Definition, AgentVersion, error) {
	return l.RestoreAgentVersionInWorkspace(PersonalWorkspaceID, dir, id, version, "")
}

// RestoreAgentVersionInWorkspace rolls one workspace's agent back to one of
// its own snapshots, recording the actor who did it.
func (l *Loader) RestoreAgentVersionInWorkspace(workspaceID, dir, id, version, actor string) (*agent.Definition, AgentVersion, error) {
	workspaceID = NormalizeWorkspace(workspaceID)
	data, v, err := l.ReadAgentVersionInWorkspace(workspaceID, id, version)
	if err != nil {
		return nil, AgentVersion{}, err
	}
	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, AgentVersion{}, fmt.Errorf("parse version YAML: %w", err)
	}
	def.ID = id
	if existing := l.GetInWorkspace(workspaceID, id); existing != nil {
		def.SourcePath = existing.SourcePath
		def.LoadedAt = existing.LoadedAt
	}
	if dir == "" && len(l.dirs) > 0 {
		dir = l.dirs[0]
	}
	if err := l.UpsertInWorkspace(workspaceID, dir, &def, actor); err != nil {
		return nil, AgentVersion{}, err
	}
	return &def, v, nil
}

func (l *Loader) snapshotPath(workspaceID, dir, id, sourcePath, actor string) error {
	if sourcePath == "" || sourcePath == builtinSourcePath {
		return nil
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	roots := l.historyRoots(workspaceID, dir)
	if len(roots) == 0 {
		return nil
	}
	hdir := filepath.Join(roots[0], id)
	if err := os.MkdirAll(hdir, 0755); err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	name := createdAt.Format("20060102T150405.000000000Z")
	if err := os.WriteFile(filepath.Join(hdir, name+".yaml"), data, 0644); err != nil {
		return err
	}
	// The snapshot itself stays a plain YAML file so it remains readable and
	// restorable by hand. Provenance goes in a sidecar rather than inside the
	// YAML, so a restored definition is byte-identical to what was deployed.
	metadata, err := json.Marshal(versionMetadata{Actor: actor, WorkspaceID: NormalizeWorkspace(workspaceID), CreatedAt: createdAt})
	if err != nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(hdir, name+".meta.json"), metadata, 0600); err != nil {
		l.log.Warn("agent version metadata not recorded", zap.String("agent", id), zap.Error(err))
	}
	return nil
}

// applyVersionMetadata overlays the recorded actor and creation time when a
// sidecar exists. Snapshots taken before provenance was recorded keep their
// mtime-derived timestamp and an empty actor rather than a fabricated one.
func applyVersionMetadata(version *AgentVersion, snapshotPath string) {
	raw, err := os.ReadFile(strings.TrimSuffix(snapshotPath, ".yaml") + ".meta.json")
	if err != nil {
		return
	}
	var metadata versionMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return
	}
	version.Actor = metadata.Actor
	if metadata.WorkspaceID != "" {
		version.WorkspaceID = metadata.WorkspaceID
	}
	if !metadata.CreatedAt.IsZero() {
		version.CreatedAt = metadata.CreatedAt.UTC()
	}
}

// historyRoots returns the snapshot directories for one workspace, preferred
// directory first. Each workspace's history lives under its own agent root, so
// listing versions can only ever surface that workspace's definitions.
func (l *Loader) historyRoots(workspaceID, preferredDir string) []string {
	workspaceID = NormalizeWorkspace(workspaceID)
	var roots []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		root := filepath.Join(workspaceAgentRoot(dir, workspaceID), ".agent-history")
		for _, existing := range roots {
			if existing == root {
				return
			}
		}
		roots = append(roots, root)
	}
	add(preferredDir)
	for _, dir := range l.dirs {
		add(dir)
	}
	return roots
}
