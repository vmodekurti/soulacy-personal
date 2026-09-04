// workspace_paths.go — shared helpers so every `sy` subcommand resolves
// install paths through the SAME function the gateway uses.
//
// Before this existed, four `sy` files hardcoded ~/.soulacy/... constants
// (skills dir, agents dir, runtime dir, setup root). On the modern
// soulspace layout (~/.soulacy/soulspace/), those hardcoded paths point
// at nothing — `sy pull` lands agents where the gateway can't see them,
// `sy skill install` puts skills in the wrong directory, `sy setup`
// prints wrong instructions. The fix is to make `config.ResolveWorkspace`
// the only place that knows the layout; every consumer asks here.
//
// Pattern for new sy subcommands:
//
//	ws := syWorkspace()           // typed Paths, never panics
//	dirs := []string{ws.Agents}   // or ws.Skills, ws.Templates, etc.
//
// `syWorkspace()` falls back to the legacy flat layout if the workspace
// resolver fails (e.g. $HOME unset in a weird shell) — matches how
// `config.Load` behaves so sy and the gateway resolve identically even
// in pathological environments.

package main

import (
	"os"
	"path/filepath"

	"github.com/soulacy/soulacy/internal/config"
)

// syWorkspace returns the resolved workspace Paths for sy subcommands.
// Never panics. On error from config.ResolveWorkspace (unusual — only
// fires if os.UserHomeDir fails), falls back to ~/.soulacy as the root.
// Callers should NOT introspect the legacy-vs-soulspace distinction —
// just read the field they need (ws.Agents, ws.Skills, etc.).
func syWorkspace() config.Paths {
	ws, err := config.ResolveWorkspace()
	if err == nil && ws.Root != "" {
		return ws
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		// Last-ditch — relative paths so the caller at least gets
		// non-empty fields. They'll fail at OS open time with a clear
		// error rather than silently truncating to "".
		home = "."
	}
	root := filepath.Join(home, ".soulacy")
	return config.Paths{
		Root:       root,
		Legacy:     true,
		Agents:     filepath.Join(root, "agents"),
		Skills:     filepath.Join(root, "skills"),
		Plugins:    filepath.Join(root, "plugins"),
		Templates:  filepath.Join(root, "templates"),
		Tools:      filepath.Join(root, "tools"),
		Memory:     filepath.Join(root, "memory"),
		Data:       root,
		Logs:       filepath.Join(root, "logs"),
		Audit:      filepath.Join(root, "audit"),
		Secrets:    filepath.Join(root, "secrets"),
		Registry:   filepath.Join(root, "registry"),
		GUI:        filepath.Join(root, "gui"),
		ConfigFile: filepath.Join(root, "config.yaml"),
	}
}
