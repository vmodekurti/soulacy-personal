package plugins

// stores.go — one plugin Loader per workspace (MU-017 criterion 1).
//
// A plugin contributes tools that agents can call, so a single process-wide
// loader means every workspace's agents can invoke every workspace's plugins.
// Plugins are ordinarily organization-owned rather than workspace-owned — the
// ownership catalog classifies them that way — but "organization-owned" is not
// "deployment-wide", and until an organization has its own extension shelf the
// safe reading of a shared loader is that it is nobody's.
//
// The scan list layers exactly like skills: configured platform directories
// stay read-only templates available to every workspace, and each workspace's
// own directory is scanned last so it can shadow a platform plugin by name
// without touching the platform copy.

import (
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// Stores is the per-workspace registry of plugin loaders.
type Stores struct {
	platformDirs []string
	base         string

	mu      sync.Mutex
	loaders map[string]*Loader
	log     *zap.Logger
}

// NewStores builds the registry. base is the directory under which each
// workspace's own plugins live; personal resolves to base itself, so a
// single-user installation is unchanged.
func NewStores(platformDirs []string, base string, log *zap.Logger) *Stores {
	return &Stores{
		platformDirs: append([]string(nil), platformDirs...),
		base:         strings.TrimSpace(base),
		loaders:      map[string]*Loader{},
		log:          log,
	}
}

// Dir is the only directory a workspace's plugins may be installed into.
func (s *Stores) Dir(workspaceID string) string {
	if s == nil || s.base == "" {
		return ""
	}
	return wsroot.Dir(s.base, wsroot.Normalize(workspaceID))
}

// ScanDirs is the ordered scan list for one workspace.
func (s *Stores) ScanDirs(workspaceID string) []string {
	if s == nil {
		return nil
	}
	dirs := append([]string(nil), s.platformDirs...)
	if own := s.Dir(workspaceID); own != "" {
		dirs = append(dirs, own)
	}
	return dirs
}

// For returns one workspace's loader, scanning on first use.
func (s *Stores) For(workspaceID string) *Loader {
	if s == nil {
		return nil
	}
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.loaders[workspaceID]; ok {
		return existing
	}
	loader := New(s.ScanDirs(workspaceID), s.log)
	s.loaders[workspaceID] = loader
	return loader
}

// EnsureDir creates a workspace's own plugin directory for an install.
func (s *Stores) EnsureDir(workspaceID string) (string, error) {
	dir := s.Dir(workspaceID)
	if dir == "" {
		return "", os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
