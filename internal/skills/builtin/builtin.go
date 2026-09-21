// Package builtin ships Soulacy's own skill catalog inside the binary and
// seeds it into the workspace, the way agent templates are embedded.
//
// Skills are plain directories the loader scans, so "shipping" one means
// writing it to <workspace>/skills/<name>/ where the person can read, edit,
// or delete it like any other. Two rules keep that safe across upgrades:
//
//   - a skill is (re)written only when it is absent or still exactly what we
//     wrote last time (every file's hash matches the manifest) — so a new
//     release refreshes our copy but never a person's edits;
//   - a directory we did not create (no manifest entry) is never touched,
//     even if it carries the same name.
//
// Each seeded directory carries a .soulacy-builtin marker so the API and the
// Skills page can label it.
package builtin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
)

//go:embed all:catalog
var catalog embed.FS

const (
	// Marker is the file that marks a seeded, built-in skill directory.
	Marker = ".soulacy-builtin"
	// ManifestFile records, per relative path, the hash of what we wrote.
	ManifestFile = ".builtin-manifest.json"
)

// Names lists the skills in the embedded catalog, sorted.
func Names() []string {
	entries, err := fs.ReadDir(catalog, "catalog")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := fs.Stat(catalog, "catalog/"+e.Name()+"/SKILL.md"); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names
}

// Files returns the embedded files of one skill, keyed by path relative to
// the skill directory.
func Files(name string) (map[string][]byte, error) {
	root := "catalog/" + name
	if _, err := fs.Stat(catalog, root+"/SKILL.md"); err != nil {
		return nil, fmt.Errorf("built-in skill %q: %w", name, err)
	}
	out := map[string][]byte{}
	err := fs.WalkDir(catalog, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := catalog.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[strings.TrimPrefix(p, root+"/")] = b
		return nil
	})
	return out, err
}

// Result says what Seed did.
type Result struct {
	Written []string // skills written or refreshed
	Kept    []string // skills left alone: edited by the person, or not ours
}

// Seed writes the catalog into skillsDir according to the rules above.
func Seed(skillsDir string, log *zap.Logger) (Result, error) {
	if log == nil {
		log = zap.NewNop()
	}
	var res Result
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return res, err
	}
	manifest, err := readManifest(skillsDir)
	if err != nil {
		return res, err
	}
	for _, name := range Names() {
		files, err := Files(name)
		if err != nil {
			return res, err
		}
		dir := filepath.Join(skillsDir, name)
		switch ours, why := isOurs(dir, name, manifest); {
		case !ours:
			res.Kept = append(res.Kept, name)
			log.Info("built-in skill left alone", zap.String("skill", name), zap.String("reason", why))
			continue
		}
		if unchanged(dir, name, files, manifest) {
			continue
		}
		if err := writeSkill(dir, files); err != nil {
			return res, fmt.Errorf("seed %s: %w", name, err)
		}
		for rel, b := range files {
			manifest[name+"/"+rel] = hashOf(b)
		}
		res.Written = append(res.Written, name)
	}
	if err := writeManifest(skillsDir, manifest); err != nil {
		return res, err
	}
	return res, nil
}

// isOurs reports whether dir is absent or a directory this package wrote
// and the person has not edited since.
func isOurs(dir, name string, manifest map[string]string) (bool, string) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return true, ""
	}
	if _, err := os.Stat(filepath.Join(dir, Marker)); err != nil {
		return false, "directory exists but was not created by Soulacy"
	}
	edited := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == Marker {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		want, known := manifest[name+"/"+filepath.ToSlash(rel)]
		b, rerr := os.ReadFile(p)
		if !known || rerr != nil || hashOf(b) != want {
			edited = true
		}
		return nil
	})
	if edited {
		return false, "edited by the person since it was seeded"
	}
	return true, ""
}

func unchanged(dir, name string, files map[string][]byte, manifest map[string]string) bool {
	if _, err := os.Stat(filepath.Join(dir, Marker)); err != nil {
		return false
	}
	for rel, b := range files {
		if manifest[name+"/"+rel] != hashOf(b) {
			return false
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return false
		}
	}
	return true
}

func writeSkill(dir string, files map[string][]byte) error {
	// Replace wholesale: a stale file from an older catalog must not linger.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for rel, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(rel, "scripts/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, b, mode); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, Marker), []byte("seeded by Soulacy; edit freely — an edited skill is never overwritten by upgrades\n"), 0o644)
}

// IsBuiltin reports whether a skill directory was seeded from the catalog.
func IsBuiltin(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, Marker))
	return err == nil
}

func hashOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func readManifest(skillsDir string) (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(skillsDir, ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return m, nil
}

func writeManifest(skillsDir string, m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillsDir, ManifestFile), b, 0o644)
}
