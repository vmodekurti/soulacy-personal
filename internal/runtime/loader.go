// Package runtime implements the agent execution engine.
// The Loader watches agent directories and hot-reloads SOUL.yaml files
// whenever they change. No restart required to deploy or update an agent.
package runtime

import (
	"bytes"
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

// Loader discovers and hot-reloads agent definitions from disk.
type Loader struct {
	dirs   []string
	agents map[string]*agent.Definition
	mu     sync.RWMutex
	log    *zap.Logger
}

// AgentVersion is one immutable SOUL.yaml snapshot captured before an agent is
// overwritten or deleted.
type AgentVersion struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	Bytes     int       `json:"bytes"`
}

// NewLoader creates a Loader that watches the given directories.
func NewLoader(dirs []string) *Loader {
	l := &Loader{
		dirs:   dirs,
		agents: make(map[string]*agent.Definition),
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
	l.agents[system.ID] = system
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
		ConfirmTools: []string{"shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"},
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

**"Install and configure X for me"**
1. fetch_url the project URL or docs link to read setup instructions.
2. install_library or shell_exec to install.
3. read_file ~/.soulacy/config.yaml to see the current config.
4. write_file to add the new configuration block, preserving existing content.
5. Tell the user exactly what changed and what (if anything) they need to do manually (e.g. browser OAuth, API key entry).

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
	found := map[string]bool{}

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
					def.ConfirmTools = []string{"shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"}
				}
				def.SourcePath = path
				l.agents[SystemAgentID] = def
				found[SystemAgentID] = true
				return nil
			}

			l.agents[def.ID] = def
			found[def.ID] = true
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("walk %s: %w", dir, err))
		}
	}

	// Remove agents whose files have been deleted.
	// Built-in agents (SourcePath == builtinSourcePath) are permanent — they
	// live only in memory and are never written to disk, so we skip them here.
	for id, def := range l.agents {
		if def.SourcePath == builtinSourcePath {
			continue // never prune built-ins
		}
		if !found[id] {
			if id == SystemAgentID {
				l.agents[SystemAgentID] = builtinSystemAgent()
			} else {
				delete(l.agents, id)
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
	if id == SystemAgentID {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	d, ok := l.agents[id]
	return ok && d.SourcePath == builtinSourcePath
}

// Get returns the agent definition with the given ID, or nil if not found.
//
// Returns a deep clone of the stored Definition so a hot-reload mid-run
// cannot mutate the pointer the engine is holding. Slice and map fields
// each get their own backing storage. See agent.Definition.Clone().
func (l *Loader) Get(id string) *agent.Definition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	d, ok := l.agents[id]
	if !ok {
		return nil
	}
	return d.Clone()
}

// All returns a snapshot of all loaded agent definitions. Each definition is
// a deep clone (same rationale as Get — see agent.Definition.Clone()).
func (l *Loader) All() []*agent.Definition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	defs := make([]*agent.Definition, 0, len(l.agents))
	for _, d := range l.agents {
		defs = append(defs, d.Clone())
	}
	return defs
}

// SetEnabledInMemory flips the Enabled flag on the in-memory definition WITHOUT
// rewriting SOUL.yaml on disk. Used by boot-time validation (Story 2) to quarantine
// an agent whose configured model is unavailable — a hot-reload of the file will
// restore whatever the file says, which is the intended behaviour (fix the file,
// save, and it comes back). Returns false if the agent ID is unknown.
func (l *Loader) SetEnabledInMemory(id string, enabled bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.agents[id]
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
	if def.ID == "" {
		return fmt.Errorf("agent ID is required")
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
			def.ConfirmTools = []string{"shell_exec", "run_script", "write_file", "http_request", "download_file", "install_library"}
		}
	}

	oldPath := def.SourcePath // where this agent currently lives (empty for new agents/imports)
	if oldPath == "" {
		l.mu.RLock()
		if existing := l.agents[def.ID]; existing != nil {
			oldPath = existing.SourcePath
		}
		l.mu.RUnlock()
	}
	if oldPath != "" && oldPath != builtinSourcePath {
		if err := l.snapshotPath(dir, def.ID, oldPath); err != nil {
			l.log.Warn("agent history snapshot failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}

	agentDir := filepath.Join(dir, def.ID)
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
	l.agents[def.ID] = def
	l.mu.Unlock()
	return nil
}

// Register adds a definition to the in-memory registry WITHOUT touching disk, so
// the engine can run it but it is never persisted. Used for ephemeral agents like
// a Studio "try this" run. Pair every Register with an Unregister (defer).
func (l *Loader) Register(def *agent.Definition) {
	if def == nil || def.ID == "" {
		return
	}
	l.mu.Lock()
	l.agents[def.ID] = def
	l.mu.Unlock()
}

// Unregister drops an in-memory-only definition added via Register. It never
// touches disk, so it is safe even if a same-id persisted agent exists (callers
// must use a unique ephemeral id to avoid evicting a real agent).
func (l *Loader) Unregister(id string) {
	l.mu.Lock()
	delete(l.agents, id)
	l.mu.Unlock()
}

// Delete removes an agent definition from disk and memory.
func (l *Loader) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if id == SystemAgentID {
		return fmt.Errorf("agent %q is a protected built-in and cannot be deleted", id)
	}

	def, ok := l.agents[id]
	if !ok {
		// Already absent (e.g. a stale GUI row or a phantom left by an id rename).
		// Delete is idempotent — deleting something that's gone is success.
		return nil
	}
	if def.SourcePath == builtinSourcePath {
		return fmt.Errorf("agent %q is a built-in and cannot be deleted", id)
	}
	if err := l.snapshotPath("", id, def.SourcePath); err != nil {
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
	delete(l.agents, id)
	return nil
}

// AgentVersions returns snapshots for an agent, newest first.
func (l *Loader) AgentVersions(id string) ([]AgentVersion, error) {
	var out []AgentVersion
	for _, root := range l.historyRoots("") {
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
			out = append(out, AgentVersion{
				ID:        strings.TrimSuffix(entry.Name(), ".yaml"),
				AgentID:   id,
				Path:      path,
				CreatedAt: info.ModTime().UTC(),
				Bytes:     int(info.Size()),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// ReadAgentVersion returns the raw SOUL.yaml captured in a version snapshot.
func (l *Loader) ReadAgentVersion(id, version string) ([]byte, AgentVersion, error) {
	version = filepath.Base(strings.TrimSpace(version))
	if version == "." || version == "" || strings.Contains(version, string(filepath.Separator)) {
		return nil, AgentVersion{}, fmt.Errorf("invalid version id")
	}
	if !strings.HasSuffix(version, ".yaml") {
		version += ".yaml"
	}
	for _, root := range l.historyRoots("") {
		path := filepath.Join(root, id, version)
		data, err := os.ReadFile(path)
		if err == nil {
			info, _ := os.Stat(path)
			v := AgentVersion{ID: strings.TrimSuffix(filepath.Base(path), ".yaml"), AgentID: id, Path: path, Bytes: len(data)}
			if info != nil {
				v.CreatedAt = info.ModTime().UTC()
				v.Bytes = int(info.Size())
			}
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
	data, v, err := l.ReadAgentVersion(id, version)
	if err != nil {
		return nil, AgentVersion{}, err
	}
	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, AgentVersion{}, fmt.Errorf("parse version YAML: %w", err)
	}
	def.ID = id
	if existing := l.Get(id); existing != nil {
		def.SourcePath = existing.SourcePath
		def.LoadedAt = existing.LoadedAt
	}
	if dir == "" && len(l.dirs) > 0 {
		dir = l.dirs[0]
	}
	if err := l.Upsert(dir, &def); err != nil {
		return nil, AgentVersion{}, err
	}
	return &def, v, nil
}

func (l *Loader) snapshotPath(dir, id, sourcePath string) error {
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
	roots := l.historyRoots(dir)
	if len(roots) == 0 {
		return nil
	}
	hdir := filepath.Join(roots[0], id)
	if err := os.MkdirAll(hdir, 0755); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + ".yaml"
	return os.WriteFile(filepath.Join(hdir, name), data, 0644)
}

func (l *Loader) historyRoots(preferredDir string) []string {
	var roots []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		root := filepath.Join(dir, ".agent-history")
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
