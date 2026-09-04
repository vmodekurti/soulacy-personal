package pkgregistry

import (
	"context"
	"fmt"
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
	source, ok := gitSourceFor(slug)
	if !ok {
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %s: %q is not a git source: %w", p.id, slug, sdkpkg.ErrNotFound)
	}
	return sdkpkg.Package{
		Slug:     slug,
		Version:  "HEAD",
		Source:   source,
		Provider: p.id,
	}, nil
}

// Fetch shallow-clones the repository into dstDir via the shared hardened
// clone path (.git stripped, 120s timeout).
func (p *gitProvider) Fetch(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	if pkg.Source == "" {
		return fmt.Errorf("pkgregistry: %s: package %q has no git source", p.id, pkg.Slug)
	}
	return plugininstall.GitClone(ctx, pkg.Source, dstDir)
}

// gitSourceFor maps a slug to a cloneable URL. Accepted forms:
//
//	https://… / http://… / git@…          → used verbatim
//	host.tld/path/repo (e.g. github.com/user/skill) → https:// prefixed
//
// Anything else (plain package slugs) reports ok=false.
func gitSourceFor(slug string) (string, bool) {
	s := strings.TrimSpace(slug)
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "git@") {
		return s, true
	}
	// host.tld/owner/repo — first path segment must contain a dot (a domain).
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 && strings.Contains(parts[0], ".") && parts[1] != "" {
		return "https://" + s, true
	}
	return "", false
}
