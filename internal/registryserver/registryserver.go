// Package registryserver is the reference implementation of the E19 HTTP
// package-registry protocol — the server side of internal/pkgregistry's
// http provider. Run it with `soulacy registry serve --dir <packages>`.
//
// Layout: a flat directory of package archives named
// `<slug>-<version>.tar.gz` (also .tgz/.zip). Optional `<slug>.yaml`
// sidecars add metadata ({description: …}). The newest version of each
// slug (numeric dotted compare) is what Resolve returns. Checksums are
// computed at (re)index time; with an ed25519 private key configured every
// package is signed over its archive digest (see
// internal/pkgregistry/signature.go for the contract).
//
// Endpoints:
//
//	GET /v1/search?q={query}   → {"packages":[Package…]} (slug+description substring)
//	GET /v1/packages/{slug}    → Package (404 = unknown)
//	GET /archives/{file}       → the archive (traversal-guarded)
//	GET /healthz               → 200
package registryserver

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/pkgregistry"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// Server serves one packages directory.
type Server struct {
	dir  string
	priv ed25519.PrivateKey // nil = unsigned registry

	mu    sync.RWMutex
	index map[string]indexEntry // slug → latest version
}

type indexEntry struct {
	pkg  sdkpkg.Package
	file string // archive basename
}

// New indexes dir and returns a ready Server. priv, when non-nil, signs
// every package's archive digest.
func New(dir string, priv ed25519.PrivateKey) (*Server, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Server{dir: abs, priv: priv}
	if err := s.Reindex(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reindex rescans the packages directory (call after adding archives).
func (s *Server) Reindex() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("registryserver: read %s: %w", s.dir, err)
	}
	index := map[string]indexEntry{}
	for _, e := range entries {
		if e.IsDir() || !isArchiveName(e.Name()) {
			continue
		}
		slug, version := parseArchiveName(e.Name())
		if prev, ok := index[slug]; ok && !versionLess(prev.pkg.Version, version) {
			continue // keep the newer one already indexed
		}
		checksum, err := fileChecksum(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return fmt.Errorf("registryserver: checksum %s: %w", e.Name(), err)
		}
		pkg := sdkpkg.Package{
			Slug:        slug,
			Version:     version,
			Checksum:    checksum,
			Source:      "/archives/" + e.Name(), // absolutised per request
			Description: sidecarDescription(filepath.Join(s.dir, slug+".yaml")),
		}
		if s.priv != nil {
			sig, serr := pkgregistry.SignChecksum(s.priv, checksum)
			if serr != nil {
				return fmt.Errorf("registryserver: sign %s: %w", e.Name(), serr)
			}
			pkg.Signature = sig
		}
		index[slug] = indexEntry{pkg: pkg, file: e.Name()}
	}
	s.mu.Lock()
	s.index = index
	s.mu.Unlock()
	return nil
}

// Count returns the number of indexed packages.
func (s *Server) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.index)
}

func (s *Server) lookup(slug string) (sdkpkg.Package, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.index[slug]
	return e.pkg, ok
}

// Handler returns the registry's http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/search", s.handleSearch)
	mux.HandleFunc("/v1/packages/", s.handlePackage)
	mux.HandleFunc("/archives/", s.handleArchive)
	return mux
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	s.mu.RLock()
	var pkgs []sdkpkg.Package
	for _, e := range s.index {
		if q == "" ||
			strings.Contains(strings.ToLower(e.pkg.Slug), q) ||
			strings.Contains(strings.ToLower(e.pkg.Description), q) {
			pkgs = append(pkgs, absolutise(e.pkg, r))
		}
	}
	s.mu.RUnlock()
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Slug < pkgs[j].Slug })
	if pkgs == nil {
		pkgs = []sdkpkg.Package{}
	}
	writeJSON(w, map[string]any{"packages": pkgs})
}

func (s *Server) handlePackage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/v1/packages/")
	pkg, ok := s.lookup(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, absolutise(pkg, r))
}

// handleArchive serves archives with a strict traversal guard: the resolved
// path must be a direct child of the packages dir AND an indexed archive.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/archives/")
	// Reject anything that isn't a plain basename of an indexed archive.
	if name == "" || name != filepath.Base(name) || !isArchiveName(name) {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	known := false
	for _, e := range s.index {
		if e.file == name {
			known = true
			break
		}
	}
	s.mu.RUnlock()
	if !known {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.dir, name))
}

// absolutise rewrites the package's relative archive path into a full URL
// for the requesting client.
func absolutise(pkg sdkpkg.Package, r *http.Request) sdkpkg.Package {
	if strings.HasPrefix(pkg.Source, "/") {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		pkg.Source = scheme + "://" + r.Host + pkg.Source
	}
	return pkg
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ── indexing helpers ─────────────────────────────────────────────────────────

func isArchiveName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz") || strings.HasSuffix(n, ".zip")
}

// stripArchiveExt removes the archive extension.
func stripArchiveExt(name string) string {
	for _, ext := range []string{".tar.gz", ".tgz", ".zip"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// parseArchiveName splits `<slug>-<version>.<ext>`: the version is the
// suffix after the LAST '-' when it looks like a version (digits, dots,
// optional leading 'v'). Archives without one index as version "0.0.0".
func parseArchiveName(name string) (slug, version string) {
	base := stripArchiveExt(name)
	if i := strings.LastIndex(base, "-"); i > 0 {
		cand := base[i+1:]
		if looksLikeVersion(cand) {
			return base[:i], cand
		}
	}
	return base, "0.0.0"
}

func looksLikeVersion(s string) bool {
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

// versionLess compares dotted versions numerically ("1.2.0" < "1.10.0").
// Non-numeric parts fall back to string compare.
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var sa, sb string
		if i < len(pa) {
			sa = pa[i]
		}
		if i < len(pb) {
			sb = pb[i]
		}
		na, ea := strconv.Atoi(sa)
		nb, eb := strconv.Atoi(sb)
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return na < nb
			}
		default:
			if sa != sb {
				return sa < sb
			}
		}
	}
	return false
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sidecarDescription reads the optional `<slug>.yaml` metadata file.
func sidecarDescription(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var meta struct {
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal(data, &meta) != nil {
		return ""
	}
	return strings.TrimSpace(meta.Description)
}
