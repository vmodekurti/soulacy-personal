// Package browserlibs installs Chromium's shared libraries into the workspace
// at runtime, for deployments that cannot install system packages.
//
// The libraries a browser links against are system packages, and system
// packages need root. An unprivileged container therefore cannot obtain them
// after the fact — which leaves two bad options: bake ~30MB into every image
// on the chance somebody drives a browser, or tell most users they cannot.
//
// There is a third. The libraries do not have to be installed as packages;
// they only have to be found. Unpacked into a directory the gateway owns and
// named in LD_LIBRARY_PATH, they load exactly as well from the mounted volume
// as from /usr/lib — verified by launching Chromium in an image that has none
// of them installed.
//
// So the image ships without them, and a deployment that wants a local browser
// asks for them once. They land on the volume, so they survive a redeploy.
//
// # Integrity
//
// These files are loaded into the browser process. A tampered bundle is code
// execution, not a corrupt download, so a checksum is required rather than
// encouraged: Install refuses without one and verifies before anything is
// unpacked.
package browserlibs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DirName is the directory, inside the workspace, the bundle unpacks into.
const DirName = "browser-libs"

const manifestName = ".soulacy-bundle.json"

// Manifest records what was installed, so a later run can tell a finished
// install from a directory left behind by one that died halfway.
type Manifest struct {
	Source      string    `json:"source"`
	SHA256      string    `json:"sha256"`
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
	InstalledAt time.Time `json:"installed_at"`
}

// Dir is where the libraries live for a given workspace.
func Dir(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return ""
	}
	return filepath.Join(workspace, DirName)
}

// Installed reports a complete install, and what it was.
func Installed(workspace string) (Manifest, bool) {
	dir := Dir(workspace)
	if dir == "" {
		return Manifest{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return Manifest{}, false
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil || m.Files == 0 {
		return Manifest{}, false
	}
	return m, true
}

// Apply puts an installed bundle on the library search path of this process,
// which every stdio MCP server inherits when it starts.
//
// One environment variable covers every browser server rather than each one
// carrying its own copy of the path — a per-server setting is one more thing
// to get right in a config file, and getting it wrong fails with a linker
// error that says nothing about Soulacy.
//
// It returns whether anything was applied, so startup can log the truth.
func Apply(workspace string) bool {
	dir := Dir(workspace)
	if dir == "" {
		return false
	}
	if _, ok := Installed(workspace); !ok {
		return false
	}
	existing := os.Getenv("LD_LIBRARY_PATH")
	for _, p := range filepath.SplitList(existing) {
		if p == dir {
			return true // already there; do not grow the variable on every reload
		}
	}
	if existing == "" {
		_ = os.Setenv("LD_LIBRARY_PATH", dir)
	} else {
		_ = os.Setenv("LD_LIBRARY_PATH", dir+string(os.PathListSeparator)+existing)
	}
	return true
}

// Options configure an install.
type Options struct {
	// URL of the .tar.gz bundle. Soulacy publishes one per architecture;
	// an operator may host their own.
	URL string
	// SHA256 of the archive, lowercase hex. Required: see the package comment.
	SHA256 string
	// Client is the HTTP client to use. Nil means a default with a timeout.
	Client *http.Client
}

// Install downloads, verifies and unpacks the bundle into the workspace.
func Install(ctx context.Context, workspace string, opts Options) (Manifest, error) {
	dir := Dir(workspace)
	if dir == "" {
		return Manifest{}, fmt.Errorf("no workspace to install into")
	}
	if strings.TrimSpace(opts.URL) == "" {
		return Manifest{}, fmt.Errorf("a bundle URL is required")
	}
	want := strings.ToLower(strings.TrimSpace(opts.SHA256))
	if len(want) != 64 {
		return Manifest{}, fmt.Errorf("a sha256 checksum is required: these libraries are loaded into the browser process, so an unverified bundle is code execution")
	}

	// A bundle can come from a URL or from a file already on the box. The
	// second is not a convenience: an operator who built the bundle with
	// scripts/build-browser-libs.sh should not have to publish it somewhere
	// public before they are allowed to install it.
	var body io.ReadCloser
	if local, ok := localPath(opts.URL); ok {
		f, err := os.Open(local)
		if err != nil {
			return Manifest{}, fmt.Errorf("read bundle: %w", err)
		}
		body = f
	} else {
		client := opts.Client
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Minute}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
		if err != nil {
			return Manifest{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return Manifest{}, fmt.Errorf("download: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return Manifest{}, fmt.Errorf("download: %s returned %s", opts.URL, resp.Status)
		}
		body = resp.Body
	}
	defer func() { _ = body.Close() }()

	// To a temporary file first: the checksum has to be over the whole
	// archive before a single byte of it is unpacked.
	tmp, err := os.CreateTemp(filepath.Dir(dir), ".browser-libs-*.tar.gz")
	if err != nil {
		return Manifest{}, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), body); err != nil {
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != want {
		return Manifest{}, fmt.Errorf("checksum mismatch: expected %s, downloaded %s — nothing was installed", want, got)
	}

	// Unpack beside the target and swap, so a failure halfway leaves the
	// previous install intact rather than a half-replaced one.
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".browser-libs-staging-*")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	files, bytes, err := extract(tmpName, staging)
	if err != nil {
		return Manifest{}, err
	}
	if files == 0 {
		return Manifest{}, fmt.Errorf("the bundle contained no libraries")
	}

	m := Manifest{Source: opts.URL, SHA256: got, Files: files, Bytes: bytes, InstalledAt: time.Now().UTC()}
	data, err := json.Marshal(m)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, manifestName), data, 0o644); err != nil {
		return Manifest{}, err
	}

	_ = os.RemoveAll(dir)
	if err := os.Rename(staging, dir); err != nil {
		return Manifest{}, fmt.Errorf("install: %w", err)
	}
	Apply(workspace)
	return m, nil
}

// extract unpacks a gzipped tar into dir, flattened.
//
// Flattened on purpose: this is a bag of shared objects for one directory on
// LD_LIBRARY_PATH, not a filesystem image. It also disposes of the oldest
// archive bug — an entry named ../../etc/something escaping the directory it
// was supposed to land in — because only the base name is ever used.
func extract(archive, dir string) (files int, total int64, err error) {
	f, err := os.Open(archive)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories, symlinks and devices have no place here
		}
		name := filepath.Base(hdr.Name)
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, 0, err
		}
		n, err := io.Copy(out, tr)
		closeErr := out.Close()
		if err != nil {
			return 0, 0, err
		}
		if closeErr != nil {
			return 0, 0, closeErr
		}
		files++
		total += n
	}
	return files, total, nil
}

// localPath reports whether the source names a file on this machine, and
// where. Both file:// and a plain absolute path are accepted; anything else
// is a URL for the HTTP client to fetch.
func localPath(src string) (string, bool) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "file://") {
		return strings.TrimPrefix(src, "file://"), true
	}
	if strings.HasPrefix(src, "/") {
		return src, true
	}
	return "", false
}
