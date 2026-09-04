package config

// Workspace resolution — the "soulspace" layout.
//
// Fresh installations get ONE organized workspace instead of files
// scattered across a flat dot-directory:
//
//	~/.soulacy/soulspace/
//	├── config.yaml        # the one config file
//	├── agents/            # SOUL.yaml definitions
//	├── skills/            # installed skills
//	├── plugins/           # installed plugins (+ .staging)
//	├── templates/         # user template overrides
//	├── tools/             # shared python tools
//	├── memory/            # brain memory (episodic/semantic/procedural)
//	├── data/              # ALL databases (actions.db, archive.db, …)
//	├── logs/              # log files
//	├── audit/             # tool-call audit JSONL
//	├── secrets/           # credential vault, signing keys (0700)
//	├── registry/          # packages for `soulacy registry serve`
//	└── gui/               # optional static GUI override
//
// Resolution order:
//  1. SOULACY_WORKSPACE env var → that directory, soulspace layout.
//  2. ~/.soulacy/soulspace exists → soulspace layout (post-migration or
//     fresh install).
//  3. ~/.soulacy exists with legacy content (config.yaml / agents/ /
//     actions.db) → LEGACY layout rooted at ~/.soulacy: every path maps to
//     its historical location, so pre-soulspace installations keep working
//     bit-for-bit until the operator runs `sy workspace migrate`.
//  4. Nothing exists → soulspace layout at ~/.soulacy/soulspace
//     (directories created by EnsureDirs).

import (
	"os"
	"path/filepath"
	"strings"
)

// SoulspaceDirName is the workspace directory created for new installations.
const SoulspaceDirName = "soulspace"

// Paths locates every directory and database the framework uses. Resolve
// it once via ResolveWorkspace; explicit config values still win over
// these defaults wherever a config field exists.
type Paths struct {
	// Root is the workspace root (~/.soulacy/soulspace, $SOULACY_WORKSPACE,
	// or the legacy ~/.soulacy).
	Root string
	// Legacy is true when running on a pre-soulspace flat layout.
	Legacy bool

	Agents    string
	Skills    string
	Plugins   string
	Templates string
	Tools     string
	Memory    string
	Data      string // database directory (legacy: == Root)
	Logs      string
	Audit     string
	Secrets   string
	Registry  string
	GUI       string

	// ConfigFile is where config.yaml lives (and is created on writes).
	ConfigFile string
}

// DB returns the path of a named SQLite database ("actions" → actions.db).
func (p Paths) DB(name string) string {
	return filepath.Join(p.Data, name+".db")
}

// CredentialsDB is the encrypted vault. In the soulspace layout it lives
// under secrets/ (it holds key material, not operational data); legacy
// keeps the historical flat location.
func (p Paths) CredentialsDB() string {
	if p.Legacy {
		return filepath.Join(p.Root, "credentials.db")
	}
	return filepath.Join(p.Secrets, "credentials.db")
}

// Dirs lists every directory of the layout (for EnsureDirs / migration).
//
// The mcp-servers entry was added 2026-06-08 to fix the `sy doctor`
// warning that flagged a fresh install as misconfigured. The directory
// houses bundled / locally-installed MCP server distributions; it must
// exist (even empty) so the MCP page in the GUI can scan it without
// erroring.
func (p Paths) Dirs() []string {
	return []string{
		p.Agents, p.Skills, p.Plugins, p.Templates, p.Tools,
		p.Memory, p.Data, p.Logs, p.Audit, p.Secrets, p.Registry,
		filepath.Join(p.Root, "mcp-servers"),
	}
}

// soulspacePaths builds the organized layout rooted at root.
func soulspacePaths(root string) Paths {
	return Paths{
		Root:       root,
		Agents:     filepath.Join(root, "agents"),
		Skills:     filepath.Join(root, "skills"),
		Plugins:    filepath.Join(root, "plugins"),
		Templates:  filepath.Join(root, "templates"),
		Tools:      filepath.Join(root, "tools"),
		Memory:     filepath.Join(root, "memory"),
		Data:       filepath.Join(root, "data"),
		Logs:       filepath.Join(root, "logs"),
		Audit:      filepath.Join(root, "audit"),
		Secrets:    filepath.Join(root, "secrets"),
		Registry:   filepath.Join(root, "registry"),
		GUI:        filepath.Join(root, "gui"),
		ConfigFile: filepath.Join(root, "config.yaml"),
	}
}

// legacyPaths maps the historical flat ~/.soulacy layout.
func legacyPaths(root string) Paths {
	return Paths{
		Root:       root,
		Legacy:     true,
		Agents:     filepath.Join(root, "agents"),
		Skills:     filepath.Join(root, "skills"),
		Plugins:    filepath.Join(root, "plugins"),
		Templates:  filepath.Join(root, "templates"),
		Tools:      filepath.Join(root, "tools"),
		Memory:     filepath.Join(root, "memory"),
		Data:       root, // databases sat flat in the root
		Logs:       filepath.Join(root, "logs"),
		Audit:      filepath.Join(root, "audit"),
		Secrets:    filepath.Join(root, "secrets"),
		Registry:   filepath.Join(root, "registry"),
		GUI:        filepath.Join(root, "gui"),
		ConfigFile: filepath.Join(root, "config.yaml"),
	}
}

// hasLegacyContent reports whether dir looks like a pre-soulspace
// installation (rather than an empty or unrelated directory).
func hasLegacyContent(dir string) bool {
	for _, marker := range []string{"config.yaml", "agents", "actions.db", "skills"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// pointerFileName records a workspace location chosen at first-run setup. It
// always lives at the fixed anchor ~/.soulacy/<pointerFileName> (a one-line
// file holding the absolute workspace root) so the resolver finds a relocated
// workspace on every boot — including under launchctl/systemd, where an env
// var set in the install shell would not be present. The default install
// (~/.soulacy/soulspace) never writes this file.
const pointerFileName = "workspace"

// readWorkspacePointer returns the workspace root recorded in
// ~/.soulacy/workspace, or "" when the file is absent, empty, or names a
// directory that no longer exists (so a stale pointer falls back to defaults
// rather than silently creating an empty workspace at a dead path).
func readWorkspacePointer(dotDir string) string {
	b, err := os.ReadFile(filepath.Join(dotDir, pointerFileName))
	if err != nil {
		return ""
	}
	root := filepath.Clean(strings.TrimSpace(string(b)))
	if root == "" || root == "." {
		return ""
	}
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		return root
	}
	return ""
}

// SaveWorkspacePointer records root as the chosen workspace location at the
// fixed ~/.soulacy anchor. Called by `sy setup` only when the operator picks a
// non-default location. home is the user's home directory.
func SaveWorkspacePointer(home, root string) error {
	dotDir := filepath.Join(home, ".soulacy")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dotDir, pointerFileName),
		[]byte(filepath.Clean(strings.TrimSpace(root))+"\n"), 0o644)
}

// ClearWorkspacePointer removes the pointer file (no error if it was absent),
// returning the install to the conventional ~/.soulacy/soulspace default.
func ClearWorkspacePointer(home string) error {
	err := os.Remove(filepath.Join(home, ".soulacy", pointerFileName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ResolveWorkspace applies the resolution order documented above. It never
// creates directories — EnsureDirs does that after config load.
func ResolveWorkspace() (Paths, error) {
	if env := os.Getenv("SOULACY_WORKSPACE"); env != "" {
		return soulspacePaths(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	dotDir := filepath.Join(home, ".soulacy")
	// A first-run-selected custom location wins over the conventional layout.
	if root := readWorkspacePointer(dotDir); root != "" {
		return soulspacePaths(root), nil
	}
	soulspace := filepath.Join(dotDir, SoulspaceDirName)
	if st, err := os.Stat(soulspace); err == nil && st.IsDir() {
		return soulspacePaths(soulspace), nil
	}
	if hasLegacyContent(dotDir) {
		return legacyPaths(dotDir), nil
	}
	return soulspacePaths(soulspace), nil
}
