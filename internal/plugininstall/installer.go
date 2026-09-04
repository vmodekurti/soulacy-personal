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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/introspect"
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
	StagedID    string                 `json:"staged_id"`
	PluginID    string                 `json:"plugin_id"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Source      string                 `json:"source"`
	Checksum    string                 `json:"checksum,omitempty"`
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
	switch {
	case isGitSource(source):
		err = gitClone(ctx, source, dst)
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
func (ins *Installer) Approve(stagedID, source, checksum string) (string, error) {
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

func isGitSource(s string) bool {
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
func GitClone(ctx context.Context, url, dst string) error {
	return gitClone(ctx, url, dst)
}

// VerifyAndExtract sha256-verifies archivePath against checksum (hex,
// case-insensitive, REQUIRED) and extracts it into dst with the traversal +
// decompression-bomb guards. Exported for reuse by the package-registry
// HTTP provider (Story E19).
func VerifyAndExtract(archivePath, checksum, dst string) error {
	return verifyAndExtract(archivePath, checksum, dst)
}

func gitClone(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("plugininstall: git clone %s: %v: %s", url, err, strings.TrimSpace(string(out)))
	}
	// the clone's history is irrelevant and .git may be large
	_ = os.RemoveAll(filepath.Join(dst, ".git"))
	return nil
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
