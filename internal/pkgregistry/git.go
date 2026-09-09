package pkgregistry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/plugininstall"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// gitProvider resolves git-style sources — `github.com/user/my-skill`,
// full https URLs, or git@ remotes. It cannot search (a git host is not an
// index); Resolve answers only for slugs that LOOK like git sources and
// returns ErrNotFound otherwise so the engine falls through to real
// registries for plain slugs.
type gitProvider struct {
	id string
}

func newGitProvider(cfg map[string]any) (*gitProvider, error) {
	return &gitProvider{id: cfgmap.Str(cfg, "id", "git")}, nil
}

func (p *gitProvider) ID() string { return p.id }

// Search always returns no results — git sources are addressed, not
// discovered.
func (p *gitProvider) Search(context.Context, string) ([]sdkpkg.Package, error) {
	return nil, nil
}

func (p *gitProvider) Resolve(_ context.Context, slug string) (sdkpkg.Package, error) {
	source, ref, subdir, ok := gitSourceFor(slug)
	if !ok {
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %s: %q is not a git source: %w", p.id, slug, sdkpkg.ErrNotFound)
	}
	version := "HEAD"
	if ref != "" {
		version = ref
	}
	return sdkpkg.Package{
		Slug:         slug,
		Version:      version,
		Source:       source,
		SourceRef:    ref,
		SourceSubdir: subdir,
		Provider:     p.id,
	}, nil
}

// Fetch shallow-clones the repository into dstDir via the shared hardened
// clone path (.git stripped, 120s timeout).
func (p *gitProvider) Fetch(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	if pkg.Source == "" {
		return fmt.Errorf("pkgregistry: %s: package %q has no git source", p.id, pkg.Slug)
	}
	if pkg.SourceSubdir == "" {
		return plugininstall.GitCloneRef(ctx, pkg.Source, pkg.SourceRef, dstDir)
	}
	return fetchGitSubdir(ctx, pkg, dstDir)
}

// gitSourceFor maps a slug to a cloneable URL. Accepted forms:
//
//	https://… / http://… / git@…          → used verbatim
//	host.tld/path/repo (e.g. github.com/user/skill) → https:// prefixed
//
// Anything else (plain package slugs) reports ok=false.
func gitSourceFor(slug string) (source, ref, subdir string, ok bool) {
	s := strings.TrimSpace(slug)
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "git@") {
		if root, branch, dir, matched := githubTreeSource(s); matched {
			return root, branch, dir, true
		}
		return s, "", "", true
	}
	// host.tld/owner/repo — first path segment must contain a dot (a domain).
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 && strings.Contains(parts[0], ".") && parts[1] != "" {
		return "https://" + s, "", "", true
	}
	return "", "", "", false
}

// githubTreeSource converts a GitHub browser URL for a repository directory
// into a cloneable repository URL plus the selected ref and directory.
func githubTreeSource(raw string) (source, ref, subdir string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "tree" || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", "", "", false
	}
	dir := filepath.Clean(filepath.FromSlash(strings.Join(parts[4:], "/")))
	if dir == "." || filepath.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) {
		return "", "", "", false
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	return "https://github.com/" + parts[0] + "/" + repo + ".git", parts[3], dir, true
}

func fetchGitSubdir(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	cleanSubdir := filepath.Clean(pkg.SourceSubdir)
	if cleanSubdir == "." || filepath.IsAbs(cleanSubdir) || cleanSubdir == ".." || strings.HasPrefix(cleanSubdir, ".."+string(filepath.Separator)) {
		return fmt.Errorf("pkgregistry: invalid source directory %q", pkg.SourceSubdir)
	}
	parent := filepath.Dir(dstDir)
	tmp, err := os.MkdirTemp(parent, ".git-source-")
	if err != nil {
		return fmt.Errorf("pkgregistry: create git staging directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	cloneRoot := filepath.Join(tmp, "repo")
	if err := plugininstall.GitCloneRef(ctx, pkg.Source, pkg.SourceRef, cloneRoot); err != nil {
		return err
	}
	selected := filepath.Join(cloneRoot, cleanSubdir)
	realRoot, err := filepath.EvalSymlinks(cloneRoot)
	if err != nil {
		return fmt.Errorf("pkgregistry: resolve cloned repository: %w", err)
	}
	realSelected, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return fmt.Errorf("pkgregistry: source directory %q was not found at ref %q", pkg.SourceSubdir, pkg.SourceRef)
	}
	rel, err := filepath.Rel(realRoot, realSelected)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("pkgregistry: source directory %q escapes the repository", pkg.SourceSubdir)
	}
	info, err := os.Stat(realSelected)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("pkgregistry: source path %q is not a directory", pkg.SourceSubdir)
	}
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		return fmt.Errorf("pkgregistry: destination already exists")
	}
	if err := os.Rename(realSelected, dstDir); err != nil {
		return fmt.Errorf("pkgregistry: stage source directory: %w", err)
	}
	return nil
}
