// Package plugininstall implements local-first plugin installation
// (Story E13): staging from a git URL, local directory, or checksummed
// archive; explicit approval of the manifest's requested capabilities and
// credentials BEFORE activation; enable/disable/remove; and re-approval
// prompts when an updated manifest requests new permissions.
//
// No central marketplace dependency — the metadata (source, checksum,
// approval fingerprint) is designed so a registry can layer on later.
//
// Trust model: a plugin dir WITHOUT install metadata was placed there by
// the operator over the filesystem and is implicitly approved (nothing
// changes for hand-installed plugins). A dir WITH metadata is
// installer-managed: it only loads while enabled AND its current manifest
// permissions match the approved fingerprint — a hostile update that
// quietly widens its grants stops loading until a human re-approves.
package plugininstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/soulacy/soulacy/pkg/plugin"
)

// MetaFile is the per-plugin install metadata file, written into the
// plugin's directory on approval.
const MetaFile = ".soulacy-install.json"

// Meta records how a plugin was installed and what the operator approved.
type Meta struct {
	// Source is the install origin (git URL, archive path, local dir).
	Source string `json:"source"`
	// Checksum is the sha256 the operator supplied for archive installs.
	Checksum string `json:"checksum,omitempty"`
	// ApprovedFingerprint is Fingerprint() of the permissions+credentials
	// the operator explicitly approved.
	ApprovedFingerprint string `json:"approved_fingerprint"`
	// Enabled gates loading; disable keeps files on disk but skips the
	// plugin at boot.
	Enabled bool `json:"enabled"`
	// InstalledAt / ApprovedAt are audit timestamps.
	InstalledAt time.Time `json:"installed_at"`
	ApprovedAt  time.Time `json:"approved_at"`
}

// ReadMeta loads the install metadata from a plugin directory. ok=false
// means the plugin is not installer-managed (hand-installed).
func ReadMeta(dir string) (Meta, bool) {
	data, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, false
	}
	return m, true
}

func writeMeta(dir string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, MetaFile), data, 0600)
}

// Fingerprint canonically hashes a manifest's requested permissions and
// credentials. Order-insensitive: reordering grants in plugin.yaml is not a
// permission change; adding, widening, or re-scoping one is.
func Fingerprint(perms []plugin.Permission, creds []plugin.CredentialRef) string {
	p := append([]plugin.Permission(nil), perms...)
	for i := range p {
		sort.Strings(p[i].Agents)
		sort.Strings(p[i].Channels)
		sort.Strings(p[i].Types)
	}
	sort.Slice(p, func(i, j int) bool { return p[i].Cap < p[j].Cap })
	c := append([]plugin.CredentialRef(nil), creds...)
	sort.Slice(c, func(i, j int) bool { return c[i].Key < c[j].Key })

	data, _ := json.Marshal(struct {
		P []plugin.Permission    `json:"p"`
		C []plugin.CredentialRef `json:"c"`
	}{p, c})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Verdict is the loader gate decision for one plugin directory.
type Verdict struct {
	Load   bool
	Reason string // human-readable when Load is false
}

// Gate decides whether the loader should activate the plugin in dir given
// its current manifest permissions. Hand-installed plugins (no metadata)
// always load.
func Gate(dir string, perms []plugin.Permission, creds []plugin.CredentialRef) Verdict {
	meta, managed := ReadMeta(dir)
	if !managed {
		return Verdict{Load: true}
	}
	if !meta.Enabled {
		return Verdict{Load: false, Reason: "disabled by operator"}
	}
	if fp := Fingerprint(perms, creds); fp != meta.ApprovedFingerprint {
		return Verdict{Load: false, Reason: fmt.Sprintf(
			"manifest permissions changed since approval (approved %.8s…, current %.8s…) — re-approve in the GUI",
			meta.ApprovedFingerprint, fp)}
	}
	return Verdict{Load: true}
}
