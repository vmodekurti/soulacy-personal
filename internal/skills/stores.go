package skills

// stores.go — one skill Loader per workspace (MU-017 criterion 1).
//
// Skills are executable instructions an agent follows, so their inventory is
// not metadata about a tenant — it is part of what that tenant's agents *do*.
// A single process-wide loader means installing a skill in one workspace adds
// it to every agent in the deployment, and a name collision between two
// tenants' skills is resolved by scan order rather than by ownership.
//
// The scan list is layered rather than replaced. Platform directories — the
// ones a deployment operator populates, and the cross-client ~/.agents and
// project-level paths — are read-only templates visible to every workspace.
// Each workspace then gets its own directory *last*, so it can shadow a
// platform skill by name without being able to modify the platform copy, and
// installs land only in the workspace's own directory.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Stores is the per-workspace registry of skill loaders.
type Stores struct {
	platformDirs []string
	base         string
	layout       wsroot.Layout

	mu      sync.Mutex
	loaders map[string]*Loader
	log     *zap.Logger
}

// PurgeWorkspace removes one named workspace's writable skill inventory and
// drops its cached loader. Platform skill templates are never below this path
// and therefore cannot be removed by a tenant deletion.
func (s *Stores) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if s == nil || s.base == "" {
		return workspacepurge.Removed{}, nil
	}
	workspaceID = wsroot.Normalize(workspaceID)
	var (
		removed workspacepurge.Removed
		err     error
	)
	if s.layout.Root() != "" {
		removed, err = workspacepurge.PurgeLayoutTree(ctx, s.layout, s.base, workspaceID)
	} else {
		removed, err = workspacepurge.PurgeTree(ctx, s.base, workspaceID)
	}
	if err != nil {
		return removed, err
	}
	s.mu.Lock()
	delete(s.loaders, workspaceID)
	s.mu.Unlock()
	removed.Note = "workspace-installed skills"
	return removed, nil
}

func (s *Stores) SetWorkspaceLayoutRoot(root string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layout = wsroot.NewLayout(root)
	s.loaders = map[string]*Loader{}
}

// NewStores builds the registry from the platform scan list and the directory
// under which each workspace's own skills live.
//
// base is normally the deployment's skills directory: personal resolves to it
// unchanged, so a single-user installation's skills stay exactly where they are
// and in the same priority position (product invariant 7).
func NewStores(platformDirs []string, base string, log *zap.Logger) *Stores {
	return &Stores{
		platformDirs: append([]string(nil), platformDirs...),
		base:         strings.TrimSpace(base),
		loaders:      map[string]*Loader{},
		log:          log,
	}
}

// Dir is the directory a workspace's own skills are installed into. It is the
// only writable location for that tenant.
func (s *Stores) Dir(workspaceID string) string {
	if s == nil || s.base == "" {
		return ""
	}
	return s.layout.Dir(s.base, wsroot.Normalize(workspaceID))
}

// ScanDirs is the ordered scan list for one workspace: platform templates
// first, the workspace's own directory last so it wins on a name collision.
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
	loader := NewWithDirs(s.ScanDirs(workspaceID), s.log)
	loader.Scan()
	s.loaders[workspaceID] = loader
	return loader
}

// Rescan re-reads one workspace's directories. Installing a skill is a write
// to disk, so the loader has to be told; rescanning every workspace would make
// one tenant's install a latency cost for all of them.
func (s *Stores) Rescan(workspaceID string) []error {
	loader := s.For(workspaceID)
	if loader == nil {
		return nil
	}
	return loader.Scan()
}

// EnsureDir creates a workspace's own skills directory, for an install that is
// about to write into it.
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

// PlatformSkill reports whether name resolves to a platform template rather
// than to something this workspace installed.
//
// Callers use it to refuse a delete or an overwrite: a platform skill is
// shared by every workspace, so letting one tenant remove it would be a
// cross-tenant write through a route that looks local.
func (s *Stores) PlatformSkill(workspaceID, name string) bool {
	if s == nil {
		return false
	}
	own := s.Dir(workspaceID)
	for _, dir := range s.platformDirs {
		if dir == own {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
