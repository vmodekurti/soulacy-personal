package plugins

// stores.go — one plugin Loader per workspace (MU-017 criterion 1).
//
// A plugin contributes tools that agents can call, so a single process-wide
// loader means every workspace's agents can invoke every workspace's plugins.
// An installed/enabled plugin instance is workspace-owned. A shared platform
// directory is only a read-only catalog of operator-approved plugin packages;
// it does not make another workspace's installation or settings visible.
//
// The scan list layers exactly like skills: configured platform directories
// stay read-only templates available to every workspace, and each workspace's
// own directory is scanned last so it can shadow a platform plugin by name
// without touching the platform copy.

import (
	"context"
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Stores is the per-workspace registry of plugin loaders.
type Stores struct {
	platformDirs []string
	base         string
	layout       wsroot.Layout

	mu      sync.Mutex
	loaders map[string]*Loader
	log     *zap.Logger
	// settings is the live plugins_config, applied to every loader this
	// registry builds.
	//
	// HELD HERE BECAUSE For BUILDS LOADERS LAZILY, and that is what made its
	// absence a bug rather than an omission. Settings were attached once, by
	// plugins.Wire, to the loaders that existed at boot. Invalidate then
	// started discarding loaders so a lifecycle change could take effect — and
	// the replacement For built was a fresh scan with no settings on it. So
	// revoking one plugin silently stripped every OTHER plugin's configuration
	// in that workspace, until a restart put it back. The symptom is a plugin
	// that stops working for a reason unrelated to anything anybody changed.
	settings map[string]map[string]any
	// tenantSettings turns on the rule that a credential-looking value in the
	// shared plugins_config must come from the WORKSPACE — see
	// tenantsettings.go. Set explicitly by the wiring rather than defaulting
	// on, because a personal installation has one tenant whose credentials
	// genuinely are the operator's.
	//
	// Separate from settingsResolver being non-nil so that "required, and the
	// resolver is absent or broken" fails CLOSED: the value is withheld rather
	// than inherited.
	tenantSettings   bool
	settingsResolver SettingsResolver
	// withheldSettings records, per workspace, the values not handed over.
	// Read by the API and logged at boot, because a plugin quietly missing a
	// setting it expects is the kind of failure nobody traces back to a
	// security control.
	withheldSettings map[string][]WithheldSetting
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
	return s.layout.Dir(s.base, wsroot.Normalize(workspaceID))
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
	s.applySettings(workspaceID, loader)
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

// Invalidate drops a workspace's cached loader so the next For rescans.
//
// MU-017 criterion 7: "Revocation prevents new invocations." Without this the
// loader was scanned once per workspace and kept forever, so removing,
// disabling or re-approving a plugin changed the files on disk and nothing
// else — the tool stayed callable until the gateway restarted. The API told
// the operator to restart, which is a note, not a revocation: an extension
// somebody has just decided to stop trusting keeps running for as long as the
// process does.
//
// Invalidating rather than mutating the live loader in place is deliberate.
// Rescanning derives the whole answer from the directories, which is where the
// shadowing rule and the platform/workspace layering already live; applying a
// removal directly to the cached loader would be a second, narrower
// implementation of "what does this workspace have" that has to agree with the
// scan and would not, the first time a platform plugin was shadowed.
//
// It does NOT stop a call already in flight. A plugin tool is a short-lived
// subprocess bounded by the tool timeout, so "drain existing" is satisfied by
// construction; there is no long-running process here to terminate, which is
// the difference between this and the MCP transport's RemoveServer.
func (s *Stores) Invalidate(workspaceID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.loaders, wsroot.Normalize(workspaceID))
}

// PurgeWorkspace removes only one named workspace's installed plugin tree and
// its cached, derived state. Platform catalog directories are never targets,
// and workspacepurge.PurgeTree refuses the personal workspace because it
// resolves to the shared base directory.
func (s *Stores) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if s == nil || s.base == "" {
		return workspacepurge.Removed{}, os.ErrInvalid
	}
	removed, err := workspacepurge.PurgeLayoutTree(ctx, s.layout, s.base, workspaceID)
	if err != nil {
		return removed, err
	}

	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	delete(s.loaders, workspaceID)
	delete(s.withheldSettings, workspaceID)
	s.mu.Unlock()
	removed.Note = "workspace-installed plugins and cached plugin state"
	return removed, nil
}

// SetSettings installs the live plugins_config and re-applies it to every
// loader already built.
//
// Both halves matter. Storing it makes future loaders correct; re-applying it
// makes the CURRENT ones correct, and without that a plugins_config edit would
// only reach a workspace whose loader happened to be rebuilt afterwards — which
// is a rule about cache timing, not about configuration.
func (s *Stores) SetSettings(settings map[string]map[string]any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
	for workspaceID, loader := range s.loaders {
		s.applySettings(workspaceID, loader)
	}
}

// RequireTenantSettings makes credential-looking plugins_config values come
// from the workspace rather than from the operator's config.
//
// A boolean plus a resolver rather than a deployment mode, for the same reason
// the MCP pool takes one: internal/plugins has no business knowing what mode
// the process runs in.
func (s *Stores) RequireTenantSettings(resolver SettingsResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenantSettings = true
	s.settingsResolver = resolver
	// Re-applied to loaders already built. Without this the rule would only
	// reach a workspace whose loader happened to be rebuilt afterwards, which
	// is a rule about cache timing rather than about credentials.
	for workspaceID, loader := range s.loaders {
		s.applySettings(workspaceID, loader)
	}
}

// WithheldSettings returns the shared settings this workspace did not receive.
func (s *Stores) WithheldSettings(workspaceID string) []WithheldSetting {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.withheldSettings[wsroot.Normalize(workspaceID)]
	out := make([]WithheldSetting, len(items))
	copy(out, items)
	return out
}

// applySettings attaches the plugins_config sections to a loader's plugins.
// Caller holds s.mu.
func (s *Stores) applySettings(workspaceID string, loader *Loader) {
	if loader == nil || len(s.settings) == 0 {
		return
	}
	workspaceID = wsroot.Normalize(workspaceID)
	var withheld []WithheldSetting
	for _, lp := range loader.All() {
		if lp == nil || lp.Manifest.ID == "" {
			continue
		}
		section, ok := s.settings[lp.Manifest.ID]
		if !ok {
			continue
		}
		if !s.tenantSettings {
			lp.Settings = section
			continue
		}
		tenantized, missing := tenantizeSettings(lp.Manifest.ID, workspaceID, section, s.settingsResolver)
		lp.Settings = tenantized
		withheld = append(withheld, missing...)
	}
	if s.withheldSettings == nil {
		s.withheldSettings = make(map[string][]WithheldSetting)
	}
	if len(withheld) == 0 {
		delete(s.withheldSettings, workspaceID)
		return
	}
	s.withheldSettings[workspaceID] = withheld
	s.log.Warn("plugin settings withheld from a workspace",
		zap.String("workspace_id", workspaceID),
		zap.String("detail", WithheldSettingsMessage(withheld)))
}
