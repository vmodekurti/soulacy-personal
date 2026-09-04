// Package wsmigrate moves a legacy flat ~/.soulacy installation into the
// organized soulspace workspace (`sy workspace migrate`).
//
// The migration is a planned, explicit operation — never automatic. Plan()
// computes every move; Apply() executes them with os.Rename (same
// filesystem by construction: soulspace lives INSIDE ~/.soulacy), rewrites
// absolute legacy paths inside config.yaml, and leaves anything it does not
// recognise exactly where it was (reported in Plan.LeftInPlace). Stop the
// gateway before migrating — SQLite databases move as files, WAL/SHM
// siblings included.
package wsmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// Move is one planned rename.
type Move struct {
	From string
	To   string
}

// MigrationPlan describes everything Apply will do.
type MigrationPlan struct {
	From        string
	To          string
	Moves       []Move
	LeftInPlace []string // entries that stay in the legacy root
	ConfigFile  string   // legacy config.yaml ("" when absent)
}

// knownDirs map 1:1 from the legacy root into the workspace root.
var knownDirs = []string{
	"agents", "skills", "plugins", "templates", "tools",
	"memory", "logs", "audit", "secrets", "mcp-servers",
	"registry", "gui", "whatsapp-web",
}

// Plan inspects the current workspace and computes the migration. Errors
// when the installation is not legacy (fresh installs and already-migrated
// workspaces have nothing to migrate).
func Plan() (*MigrationPlan, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return nil, err
	}
	if !ws.Legacy {
		return nil, fmt.Errorf("wsmigrate: workspace at %s is already the soulspace layout — nothing to migrate", ws.Root)
	}
	from := ws.Root
	to := filepath.Join(from, config.SoulspaceDirName)

	plan := &MigrationPlan{From: from, To: to}
	entries, err := os.ReadDir(from)
	if err != nil {
		return nil, err
	}

	known := map[string]bool{}
	for _, d := range knownDirs {
		known[d] = true
	}

	for _, e := range entries {
		name := e.Name()
		switch {
		case name == config.SoulspaceDirName:
			// partial earlier attempt — Apply refuses separately
		case name == "config.yaml":
			plan.ConfigFile = filepath.Join(from, name)
			plan.Moves = append(plan.Moves, Move{From: plan.ConfigFile, To: filepath.Join(to, "config.yaml")})
		case e.IsDir() && known[name]:
			plan.Moves = append(plan.Moves, Move{From: filepath.Join(from, name), To: filepath.Join(to, name)})
		case !e.IsDir() && isDatabaseFile(name):
			// Databases (and their WAL/SHM siblings) → data/; the encrypted
			// credential vault → secrets/.
			dst := filepath.Join(to, "data", name)
			if strings.HasPrefix(name, "credentials.db") {
				dst = filepath.Join(to, "secrets", name)
			}
			plan.Moves = append(plan.Moves, Move{From: filepath.Join(from, name), To: dst})
		default:
			plan.LeftInPlace = append(plan.LeftInPlace, filepath.Join(from, name))
		}
	}
	// Directories move FIRST: file moves may target a directory (secrets/,
	// data/) that a directory rename must land on while still empty.
	sort.SliceStable(plan.Moves, func(i, j int) bool {
		di, dj := isDir(plan.Moves[i].From), isDir(plan.Moves[j].From)
		if di != dj {
			return di
		}
		return plan.Moves[i].From < plan.Moves[j].From
	})
	sort.Strings(plan.LeftInPlace)
	return plan, nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func isDatabaseFile(name string) bool {
	for _, suffix := range []string{".db", ".db-wal", ".db-shm"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// Apply executes the plan: creates the soulspace layout, renames every
// planned entry, and rewrites absolute legacy paths inside config.yaml so
// configured locations follow their files.
func Apply(plan *MigrationPlan) error {
	if st, err := os.Stat(plan.To); err == nil && st.IsDir() {
		if entries, _ := os.ReadDir(plan.To); len(entries) > 0 {
			return fmt.Errorf("wsmigrate: %s already exists and is not empty — resolve the partial migration manually", plan.To)
		}
	}
	// Build the full layout first so every destination's parent exists.
	for _, d := range soulspaceLayout(plan.To).Dirs() {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	// Rewrite config BEFORE moving it (we rewrite in place, then rename).
	if plan.ConfigFile != "" {
		if err := rewriteConfigPaths(plan.ConfigFile, plan.From, plan.To); err != nil {
			return fmt.Errorf("wsmigrate: rewrite config: %w", err)
		}
	}

	for _, m := range plan.Moves {
		if err := os.MkdirAll(filepath.Dir(m.To), 0o755); err != nil {
			return err
		}
		// The pre-created layout may already hold an EMPTY directory at the
		// destination — drop it so the legacy directory renames into place.
		if st, err := os.Stat(m.To); err == nil && st.IsDir() {
			if entries, _ := os.ReadDir(m.To); len(entries) == 0 {
				_ = os.Remove(m.To)
			}
		}
		if err := os.Rename(m.From, m.To); err != nil {
			return fmt.Errorf("wsmigrate: move %s → %s: %w", m.From, m.To, err)
		}
	}
	_ = os.Chmod(filepath.Join(plan.To, "secrets"), 0o700)
	return nil
}

// soulspaceLayout mirrors config.soulspacePaths for layout creation without
// re-resolving (the resolver still reports legacy until the markers move).
func soulspaceLayout(root string) config.Paths {
	return config.Paths{
		Root:      root,
		Agents:    filepath.Join(root, "agents"),
		Skills:    filepath.Join(root, "skills"),
		Plugins:   filepath.Join(root, "plugins"),
		Templates: filepath.Join(root, "templates"),
		Tools:     filepath.Join(root, "tools"),
		Memory:    filepath.Join(root, "memory"),
		Data:      filepath.Join(root, "data"),
		Logs:      filepath.Join(root, "logs"),
		Audit:     filepath.Join(root, "audit"),
		Secrets:   filepath.Join(root, "secrets"),
		Registry:  filepath.Join(root, "registry"),
	}
}

// rewriteConfigPaths replaces absolute legacy locations with their migrated
// equivalents inside config.yaml. String-level rewriting is deliberate: it
// preserves comments, ordering, and unknown blocks byte-for-byte.
func rewriteConfigPaths(cfgFile, from, to string) error {
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return err
	}
	s := string(data)
	// Specific mappings first (db files move INTO data/ or secrets/) …
	replacements := [][2]string{
		{filepath.Join(from, "credentials.db"), filepath.Join(to, "secrets", "credentials.db")},
	}
	for _, suffix := range []string{"actions", "archive", "knowledge", "plugins", "rbac",
		"checkpoints", "costs", "workboard", "apikeys", "dlq", "history"} {
		replacements = append(replacements, [2]string{
			filepath.Join(from, suffix+".db"), filepath.Join(to, "data", suffix+".db"),
		})
	}
	for _, d := range knownDirs {
		replacements = append(replacements, [2]string{filepath.Join(from, d), filepath.Join(to, d)})
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return os.WriteFile(cfgFile, []byte(s), 0o600)
}
