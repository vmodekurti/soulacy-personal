// Package pkgregistry implements the built-in package-registry providers
// (Story E19): an HTTP registry client and a git source resolver, plus the
// multi-registry Engine that queries configured registries in priority order
// with fallback. Providers self-register with the SDK factory registry from
// init() (register.go) and are selected by the `type` key of a config.yaml
// `registries:` entry.
package pkgregistry

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/plugininstall"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// httpProvider talks to an E19 HTTP registry:
//
//	GET {base}/v1/search?q={query}   → {"packages":[Package…]}
//	GET {base}/v1/packages/{slug}    → Package (404 = unknown slug)
//	GET {pkg.Source}                 → the package archive (tar.gz/zip)
//
// AuthHeaders from the config entry are sent verbatim on every request.
type httpProvider struct {
	id      string
	baseURL string
	headers map[string]string
	client  *http.Client
	// pubKey, when non-nil, makes signature verification MANDATORY for
	// every package fetched from this registry (config signing_key).
	pubKey ed25519.PublicKey
}

// VerifiesSignatures reports whether this registry enforces ed25519
// signatures (signing_key configured).
func (p *httpProvider) VerifiesSignatures() bool { return p.pubKey != nil }

func newHTTPProvider(cfg map[string]any) (*httpProvider, error) {
	base := strings.TrimRight(cfgmap.Str(cfg, "base_url", ""), "/")
	if base == "" {
		return nil, fmt.Errorf("pkgregistry: http registry requires base_url")
	}
	headers := map[string]string{}
	for k, v := range cfgmap.Map(cfg, "auth_headers") {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	var pubKey ed25519.PublicKey
	if hexKey := cfgmap.Str(cfg, "signing_key", ""); hexKey != "" {
		k, err := parseSigningKey(hexKey)
		if err != nil {
			return nil, err
		}
		pubKey = k
	}
	return &httpProvider{
		id:      cfgmap.Str(cfg, "id", "http"),
		baseURL: base,
		headers: headers,
		client:  &http.Client{Timeout: 60 * time.Second},
		pubKey:  pubKey,
	}, nil
}

func (p *httpProvider) ID() string { return p.id }

func (p *httpProvider) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pkgregistry: %w", err)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pkgregistry: %s: %w", p.id, err)
	}
	return resp, nil
}

func (p *httpProvider) Search(ctx context.Context, query string) ([]sdkpkg.Package, error) {
	resp, err := p.get(ctx, p.baseURL+"/v1/search?q="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pkgregistry: %s: search returned %s", p.id, resp.Status)
	}
	var out struct {
		Packages []sdkpkg.Package `json:"packages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("pkgregistry: %s: decode search response: %w", p.id, err)
	}
	return out.Packages, nil
}

func (p *httpProvider) Resolve(ctx context.Context, slug string) (sdkpkg.Package, error) {
	resp, err := p.get(ctx, p.baseURL+"/v1/packages/"+url.PathEscape(slug))
	if err != nil {
		return sdkpkg.Package{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %s: %q: %w", p.id, slug, sdkpkg.ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %s: resolve %q returned %s", p.id, slug, resp.Status)
	}
	var pkg sdkpkg.Package
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&pkg); err != nil {
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %s: decode package: %w", p.id, err)
	}
	if pkg.Slug == "" {
		pkg.Slug = slug
	}
	return pkg, nil
}

// Fetch downloads the package archive, verifies its sha256 checksum
// (REQUIRED — unverifiable archives are refused), and extracts it into
// dstDir through the hardened plugininstall extractor (path-traversal +
// decompression-bomb guards).
func (p *httpProvider) Fetch(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	if pkg.Source == "" {
		return fmt.Errorf("pkgregistry: %s: package %q has no source URL", p.id, pkg.Slug)
	}
	if strings.TrimSpace(pkg.Checksum) == "" {
		return fmt.Errorf("pkgregistry: %s: package %q has no checksum — refusing unverifiable archive", p.id, pkg.Slug)
	}
	// Signature gate: a registry with signing_key configured requires a
	// valid ed25519 signature over the archive digest on EVERY package.
	if p.pubKey != nil {
		if strings.TrimSpace(pkg.Signature) == "" {
			return fmt.Errorf("pkgregistry: %s: package %q is unsigned but this registry requires signatures", p.id, pkg.Slug)
		}
		if err := VerifySignature(p.pubKey, pkg.Checksum, pkg.Signature); err != nil {
			return fmt.Errorf("pkgregistry: %s: package %q: %w", p.id, pkg.Slug, err)
		}
	}
	resp, err := p.get(ctx, pkg.Source)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pkgregistry: %s: download %q returned %s", p.id, pkg.Slug, resp.Status)
	}

	tmp, err := os.CreateTemp("", "soulacy-pkg-*"+archiveExt(pkg.Source))
	if err != nil {
		return fmt.Errorf("pkgregistry: temp archive: %w", err)
	}
	defer os.Remove(tmp.Name())
	// Cap the download at 256 MiB — same bound the extractor enforces.
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, 256<<20)); err != nil {
		tmp.Close()
		return fmt.Errorf("pkgregistry: download %q: %w", pkg.Slug, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return plugininstall.VerifyAndExtract(tmp.Name(), pkg.Checksum, dstDir)
}

// archiveExt preserves the source extension so the extractor picks the right
// format (zip vs tar.gz).
func archiveExt(source string) string {
	s := strings.ToLower(source)
	switch {
	case strings.HasSuffix(s, ".zip"):
		return ".zip"
	case strings.HasSuffix(s, ".tgz"):
		return ".tgz"
	default:
		return ".tar.gz"
	}
}
