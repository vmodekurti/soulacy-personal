package plugininstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// stagingDirName holds staged-but-unapproved plugins inside the plugins
// root. It carries no plugin.yaml itself and staged plugins sit one level
// deeper, so the loader's one-level scan never activates them.
const stagingDirName = ".staging"

// Installer manages installer-owned plugins under one plugins root
// (typically the first configured plugin_dirs entry).
type Installer struct {
	root string
}

// New creates an Installer rooted at pluginsRoot (created if missing).
func New(pluginsRoot string) (*Installer, error) {
	if pluginsRoot == "" {
		return nil, fmt.Errorf("plugininstall: empty plugins root")
	}
	if err := os.MkdirAll(filepath.Join(pluginsRoot, stagingDirName), 0755); err != nil {
		return nil, fmt.Errorf("plugininstall: create staging dir: %w", err)
	}
	return &Installer{root: pluginsRoot}, nil
}

// Preview is what the operator approves: the staged plugin's identity and
// EVERYTHING it requests. Mirrors the manifest verbatim — the GUI renders
// it; nothing activates until Approve.
type Preview struct {
	StagedID    string `json:"staged_id"`
	PluginID    string `json:"plugin_id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Checksum    string `json:"checksum,omitempty"`
	// Revision is the exact commit a git source resolved to. The operator
	// approves this specific code, not "whatever that URL serves".
	Revision    string                 `json:"revision,omitempty"`
	Permissions []plugin.Permission    `json:"permissions"`
	Credentials []plugin.CredentialRef `json:"credentials"`
	ToolCount   int                    `json:"tool_count"`
	HasGUI      bool                   `json:"has_gui"`
	Channels    []string               `json:"channels,omitempty"`
	Providers   []string               `json:"providers,omitempty"`
	Fingerprint string                 `json:"fingerprint"`

	// Security carries the E20 pre-installation introspection report
	// (static scan + LLM audit + sandboxed dry-run). Attached by the
	// gateway after Stage when a safety pipeline is configured; nil when
	// introspection didn't run.
	Security *introspect.SecurityReport `json:"security,omitempty"`

	// Migrations are the manifest-declared schema steps (Story 17) so the
	// operator approves schema alongside permissions.
	Migrations []plugin.MigrationEntry `json:"migrations,omitempty"`
}

// StagedDir returns the on-disk path of a staged plugin so callers (the E20
// safety pipeline) can inspect it. The directory exists only between Stage
// and Approve/Discard.
func (ins *Installer) StagedDir(stagedID string) string { return ins.stagePath(stagedID) }

// ReadManifest parses the plugin.yaml in dir. Exported for the safety
// pipeline, which needs the declared sidecar hooks for its dry-run.
func ReadManifest(dir string) (plugin.Manifest, error) { return readManifest(dir) }

// Stage fetches source into the staging area and returns the approval
// preview. Source forms:
//
//   - git URL  (https://…, git@…, or anything ending in .git) → shallow clone
//   - local archive path (*.tar.gz, *.tgz, *.zip)             → extract;
//     checksum (sha256 hex) is REQUIRED and verified before extraction
//   - local directory                                          → copied
//
// Nothing under staging is ever loaded by the gateway.
func (ins *Installer) Stage(ctx context.Context, source, checksum string) (Preview, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return Preview{}, fmt.Errorf("plugininstall: empty source")
	}
	stagedID := fmt.Sprintf("stage-%d", time.Now().UnixNano())
	dst := ins.stagePath(stagedID)

	var err error
	var revision string
	switch {
	case isGitSource(source):
		revision, err = gitClone(ctx, source, dst)
	case isArchive(source):
		if checksum == "" {
			return Preview{}, fmt.Errorf("plugininstall: archive installs require a sha256 checksum")
		}
		err = verifyAndExtract(source, checksum, dst)
	default:
		if st, serr := os.Stat(source); serr == nil && st.IsDir() {
			err = copyDir(source, dst)
		} else {
			return Preview{}, fmt.Errorf("plugininstall: source %q is not a git URL, archive, or directory", source)
		}
	}
	if err != nil {
		_ = os.RemoveAll(dst)
		return Preview{}, err
	}

	m, err := readManifest(dst)
	if err != nil {
		_ = os.RemoveAll(dst)
		return Preview{}, err
	}

	pv := Preview{
		StagedID:    stagedID,
		PluginID:    m.ID,
		Name:        m.Name,
		Description: m.Description,
		Source:      source,
		Checksum:    checksum,
		Revision:    revision,
		Permissions: m.Permissions,
		Credentials: m.Credentials,
		ToolCount:   len(m.Tools),
		HasGUI:      m.GUI != nil,
		Fingerprint: Fingerprint(m.Permissions, m.Credentials),
		Migrations:  m.Migrations,
	}
	for _, ch := range m.Channels {
		pv.Channels = append(pv.Channels, ch.ID)
	}
	for _, p := range m.Providers {
		pv.Providers = append(pv.Providers, p.ID)
	}
	return pv, nil
}

// Approve activates a staged plugin: moves it into the plugins root under
// its manifest id and records the approved permission fingerprint.
func (ins *Installer) Approve(stagedID, source, checksum, revision string) (string, error) {
	src := ins.stagePath(stagedID)
	m, err := readManifest(src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(ins.root, m.ID)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("plugininstall: plugin %q already installed — remove it first", m.ID)
	}
	now := time.Now().UTC()
	if err := writeMeta(src, Meta{
		Source:              source,
		Checksum:            checksum,
		Revision:            revision,
		ApprovedFingerprint: Fingerprint(m.Permissions, m.Credentials),
		Enabled:             true,
		InstalledAt:         now,
		ApprovedAt:          now,
	}); err != nil {
		return "", fmt.Errorf("plugininstall: write metadata: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("plugininstall: activate: %w", err)
	}
	return m.ID, nil
}

// Discard removes a staged plugin without installing it.
func (ins *Installer) Discard(stagedID string) error {
	return os.RemoveAll(ins.stagePath(stagedID))
}

// Installed describes one installer-managed plugin for the management UI.
type Installed struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Source      string    `json:"source"`
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installed_at"`
	// NeedsReapproval is true when the on-disk manifest now requests
	// different permissions than were approved. The plugin does NOT load
	// until Reapprove.
	NeedsReapproval bool                   `json:"needs_reapproval"`
	Permissions     []plugin.Permission    `json:"permissions"`
	Credentials     []plugin.CredentialRef `json:"credentials"`
}

// List returns every installer-managed plugin under the root.
// Hand-installed plugins (no metadata) are not listed — they're outside the
// installer's authority.
func (ins *Installer) List() ([]Installed, error) {
	entries, err := os.ReadDir(ins.root)
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || e.Name() == stagingDirName {
			continue
		}
		dir := filepath.Join(ins.root, e.Name())
		meta, managed := ReadMeta(dir)
		if !managed {
			continue
		}
		m, merr := readManifest(dir)
		if merr != nil {
			continue
		}
		out = append(out, Installed{
			ID:              m.ID,
			Name:            m.Name,
			Source:          meta.Source,
			Enabled:         meta.Enabled,
			InstalledAt:     meta.InstalledAt,
			NeedsReapproval: Fingerprint(m.Permissions, m.Credentials) != meta.ApprovedFingerprint,
			Permissions:     m.Permissions,
			Credentials:     m.Credentials,
		})
	}
	return out, nil
}

// SetEnabled flips the enable gate (takes effect on next gateway restart).
func (ins *Installer) SetEnabled(id string, enabled bool) error {
	dir := filepath.Join(ins.root, id)
	meta, managed := ReadMeta(dir)
	if !managed {
		return fmt.Errorf("plugininstall: %q is not installer-managed", id)
	}
	meta.Enabled = enabled
	return writeMeta(dir, meta)
}

// Reapprove records the CURRENT manifest permissions as approved — the
// explicit human answer to a re-approval prompt.
func (ins *Installer) Reapprove(id string) error {
	dir := filepath.Join(ins.root, id)
	meta, managed := ReadMeta(dir)
	if !managed {
		return fmt.Errorf("plugininstall: %q is not installer-managed", id)
	}
	m, err := readManifest(dir)
	if err != nil {
		return err
	}
	meta.ApprovedFingerprint = Fingerprint(m.Permissions, m.Credentials)
	meta.ApprovedAt = time.Now().UTC()
	return writeMeta(dir, meta)
}

// Remove deletes an installer-managed plugin from disk.
func (ins *Installer) Remove(id string) error {
	dir := filepath.Join(ins.root, id)
	if _, managed := ReadMeta(dir); !managed {
		return fmt.Errorf("plugininstall: %q is not installer-managed — remove it manually", id)
	}
	return os.RemoveAll(dir)
}

// ---------------------------------------------------------------------------

func (ins *Installer) stagePath(stagedID string) string {
	// stagedID is host-generated; sanitise anyway so a crafted id can't escape.
	return filepath.Join(ins.root, stagingDirName, filepath.Base(stagedID))
}

// isGitSource classifies the source *after* stripping any `#revision`
// suffix. Without that, pinning a commit — the thing MU-017 criterion 2 asks
// for — would make the source unrecognisable and fall through to "not a git
// URL, archive, or directory".
func isGitSource(s string) bool {
	s, _ = splitGitRevision(s)
	return strings.HasPrefix(s, "https://") && !isArchive(s) ||
		strings.HasPrefix(s, "http://") && !isArchive(s) ||
		strings.HasPrefix(s, "git@") || strings.HasSuffix(s, ".git")
}

func isArchive(s string) bool {
	return strings.HasSuffix(s, ".tar.gz") || strings.HasSuffix(s, ".tgz") || strings.HasSuffix(s, ".zip")
}

// GitClone shallow-clones url into dst and strips the .git directory.
// Exported for reuse by the package-registry git provider (Story E19) so
// every git fetch in the install pipeline shares one hardened path.
// GitClone fetches url into dst and returns the commit it resolved to.
func GitClone(ctx context.Context, url, dst string) (string, error) {
	return gitClone(ctx, url, dst)
}

// VerifyAndExtract sha256-verifies archivePath against checksum (hex,
// case-insensitive, REQUIRED) and extracts it into dst with the traversal +
// decompression-bomb guards. Exported for reuse by the package-registry
// HTTP provider (Story E19).
func VerifyAndExtract(archivePath, checksum, dst string) error {
	return verifyAndExtract(archivePath, checksum, dst)
}

// splitGitRevision separates a source of the form `<url>#<revision>` into its
// parts. The revision may be a tag, a branch, or a commit SHA.
func splitGitRevision(source string) (url, revision string) {
	if i := strings.LastIndex(source, "#"); i > 0 {
		return source[:i], strings.TrimSpace(source[i+1:])
	}
	return source, ""
}

// gitClone fetches url into dst and returns the exact commit it landed on.
//
// The returned revision is the point of this function, not a nicety. A shallow
// clone of a branch is a moving target: "install this plugin from that URL"
// resolves to different code tomorrow, so an approval recorded today attests
// to nothing in particular. MU-017 criterion 2 asks the install to pin an
// immutable revision, and a commit SHA is the only identifier a git source
// offers that cannot be moved after the fact — a tag can be force-pushed, a
// branch obviously so.
//
// When the caller names a revision, that revision is fetched and checked out
// exactly; when it does not, the tip is cloned and its SHA recorded, so a
// later reinstall can be pinned to what was actually approved.
func gitClone(ctx context.Context, url, dst string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	url, revision := splitGitRevision(url)
	args := []string{"clone", "--depth", "1"}
	if revision != "" {
		// --branch takes tags and branches; a raw SHA needs the fetch path
		// below, so try the cheap form first and fall back.
		args = append(args, "--branch", revision)
	}
	args = append(args, url, dst)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if revision == "" {
			return "", fmt.Errorf("plugininstall: git clone %s: %v: %s", url, err, strings.TrimSpace(string(out)))
		}
		if ferr := gitFetchRevision(ctx, url, revision, dst); ferr != nil {
			return "", fmt.Errorf("plugininstall: git clone %s at %s: %w", url, revision, ferr)
		}
	}

	resolved, err := gitHeadRevision(ctx, dst)
	if err != nil {
		return "", err
	}
	if revision != "" && !strings.HasPrefix(resolved, revision) && resolved != revision {
		// A named tag or branch resolves to a SHA; record the SHA, which is
		// what the approval is actually about.
		_ = revision
	}
	// The clone's history is irrelevant and .git may be large. Removed only
	// after the revision has been read out of it.
	_ = os.RemoveAll(filepath.Join(dst, ".git"))
	return resolved, nil
}

// gitFetchRevision handles the case --branch cannot: a raw commit SHA.
func gitFetchRevision(ctx context.Context, url, revision, dst string) error {
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", url},
		{"fetch", "--depth", "1", "origin", revision},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, args := range steps {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dst
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func gitHeadRevision(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("plugininstall: resolve installed revision: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func verifyAndExtract(archivePath, checksum, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("plugininstall: open archive: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("plugininstall: hash archive: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(checksum)) {
		return fmt.Errorf("plugininstall: checksum mismatch: archive is %s, expected %s — refusing to install", got, checksum)
	}
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZip(archivePath, dst)
	}
	return extractTarGz(archivePath, dst)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/devices — plugins are plain trees
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

// readManifest parses and minimally validates the staged plugin.yaml.
// (Full capability/contribution validation happens in the loader at boot,
// exactly as for hand-installed plugins.)
func readManifest(dir string) (plugin.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		return plugin.Manifest{}, fmt.Errorf("plugininstall: plugin.yaml not found at the source root: %w", err)
	}
	var m plugin.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return plugin.Manifest{}, fmt.Errorf("plugininstall: parse plugin.yaml: %w", err)
	}
	if m.ID == "" {
		return plugin.Manifest{}, fmt.Errorf("plugininstall: manifest missing required field 'id'")
	}
	if m.ID == stagingDirName || strings.ContainsAny(m.ID, `/\`) || strings.HasPrefix(m.ID, ".") {
		return plugin.Manifest{}, fmt.Errorf("plugininstall: unsafe plugin id %q", m.ID)
	}
	return m, nil
}

// Installers is the per-workspace registry of installers (MU-017 criterion 1
// applied to the install path).
//
// A single installer rooted at one directory would let any workspace's
// approval activate a plugin for the whole deployment — the approval prompt
// would name one tenant's operator while the consequence lands on all of them.
// Each workspace installs into its own plugin directory, which is also the one
// the loader scans for that workspace.
type Installers struct {
	base   string
	layout wsroot.Layout

	mu         sync.Mutex
	installers map[string]*Installer
}

func (s *Installers) SetWorkspaceLayoutRoot(root string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layout = wsroot.NewLayout(root)
	s.installers = map[string]*Installer{}
}

// NewInstallers builds the registry over the directory that holds each
// workspace's plugins. Personal resolves to base itself.
func NewInstallers(base string) *Installers {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return &Installers{base: base, installers: map[string]*Installer{}}
}

// For returns one workspace's installer, creating its root on first use.
// It returns nil when the directory cannot be created — callers surface that
// as "installer unavailable" rather than falling back to a shared root, since
// the fallback would install one tenant's plugin into everybody's directory.
func (s *Installers) For(workspaceID string) *Installer {
	if s == nil {
		return nil
	}
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.installers[workspaceID]; ok {
		return existing
	}
	ins, err := New(s.layout.Dir(s.base, workspaceID))
	if err != nil {
		s.installers[workspaceID] = nil
		return nil
	}
	s.installers[workspaceID] = ins
	return ins
}

// Root reports one workspace's plugin root, for operator-facing messages.
func (s *Installers) Root(workspaceID string) string {
	if s == nil {
		return ""
	}
	return s.layout.Dir(s.base, wsroot.Normalize(workspaceID))
}
