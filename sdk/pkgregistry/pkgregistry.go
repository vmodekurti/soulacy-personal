// Package pkgregistry defines the public contracts for package-registry
// providers (Story E19) — the sources `sy skill install <slug>` and the GUI
// install flow resolve skills/plugins from.
//
// NOTE: the name is pkgregistry (not "registry") because sdk/registry is
// already the factory-registry package from Story E10. Providers register
// themselves with that factory registry (registry.RegisterPkgRegistry) and
// hosts resolve them by the `type` key of a config.yaml `registries:` entry.
//
// Compatibility: per the SDK policy (sdk/README.md) these interfaces are
// frozen within a major version — structs are append-only with zero-value
// compatibility, and the package never gains dependencies beyond stdlib.
package pkgregistry

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Resolve (and Search implementations may use it
// internally) when the registry does not know the requested slug. Hosts use
// it to fall through to the next provider in priority order; any other error
// also falls through but is surfaced in logs.
var ErrNotFound = errors.New("pkgregistry: package not found")

// Package is the metadata a registry returns for one installable package
// (a skill or plugin) at one version.
type Package struct {
	// Slug is the canonical package name, e.g. "self-improving-agent" or a
	// git-style source like "github.com/user/my-skill".
	Slug string `json:"slug"`
	// Version is the resolved version (semver tag, or a ref like "HEAD" for
	// git sources).
	Version string `json:"version"`
	// Checksum is the sha256 hex digest of the package archive. REQUIRED for
	// archive downloads (hosts refuse unverifiable archives); empty for git
	// sources, whose integrity comes from the clone itself.
	Checksum string `json:"checksum,omitempty"`
	// Signature is an optional detached signature over the archive.
	// Verification semantics are provider-specific; empty = unsigned.
	Signature string `json:"signature,omitempty"`
	// Manifest is the raw package manifest (plugin.yaml / SKILL.md
	// frontmatter) as the registry indexed it. Shape is package-owned;
	// hosts re-parse the authoritative copy after Fetch.
	Manifest map[string]any `json:"manifest,omitempty"`
	// Source is the provider-specific fetch location (archive URL, git
	// remote). Set by Resolve; consumed by Fetch.
	Source string `json:"source,omitempty"`
	// Description is an optional human-readable summary for search results.
	Description string `json:"description,omitempty"`
	// Provider is the ID of the provider that resolved this package. Hosts
	// fill it in after a successful Resolve/Search so consent dialogs can
	// show provenance.
	Provider string `json:"provider,omitempty"`
}

// Provider is one package registry. Implementations must be safe for
// concurrent use and must respect ctx cancellation on every method.
type Provider interface {
	// ID returns the stable provider id (the config entry's id).
	ID() string
	// Search returns packages matching query, best match first. An empty
	// result with nil error is valid (nothing matched).
	Search(ctx context.Context, query string) ([]Package, error)
	// Resolve returns the latest version of slug with checksum + source
	// populated. Returns ErrNotFound (possibly wrapped) when the registry
	// does not know the slug.
	Resolve(ctx context.Context, slug string) (Package, error)
	// Fetch downloads pkg into dstDir (creating it if needed) so dstDir
	// contains the package files at its root. Archive providers MUST verify
	// pkg.Checksum before extracting.
	Fetch(ctx context.Context, pkg Package, dstDir string) error
}
