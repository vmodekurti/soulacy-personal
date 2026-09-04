// probe.go — Story E26: review an arbitrary URL as a potential skill
// source and guide the user to add it to the `registries:` config block.
//
// Detection order:
//  1. known git hosts / *.git paths        → "git"   (no network round-trip)
//  2. skills.sh-style directory API        → "skillssh"
//  3. E19 HTTP registry (/v1/search)       → "http"
//  4. anything else: fetch the page and report GitHub repos it links to
//     ("unknown" — installable directly per repo, but not a registry).
//
// Both the gateway (POST /api/v1/registries/probe) and the CLI
// (`sy registry probe|add`) call Probe; the returned Suggested entry is
// what gets appended to config.yaml on consent.
package pkgregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
)

// Source kinds a probe can report.
const (
	SourceKindSkillsSh = "skillssh"
	SourceKindHTTP     = "http"
	SourceKindGit      = "git"
	SourceKindUnknown  = "unknown"
)

// ProbeReport describes what a URL turned out to be.
type ProbeReport struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	// Detail is a human-readable explanation + next-step guidance.
	Detail string `json:"detail"`
	// Samples are example skills/packages/repos found at the source.
	Samples []string `json:"samples,omitempty"`
	// HasAudits reports that the directory publishes third-party security
	// audits (skills.sh-style sources).
	HasAudits bool `json:"has_audits,omitempty"`
	// Suggested is the ready-to-add registries: entry; nil when the URL is
	// not a recognisable registry (Samples may still guide direct installs).
	Suggested *config.RegistryConfig `json:"suggested,omitempty"`
}

var probeClient = &http.Client{Timeout: 15 * time.Second}

var gitHosts = map[string]bool{
	"github.com": true, "www.github.com": true,
	"gitlab.com": true, "www.gitlab.com": true,
	"bitbucket.org": true, "codeberg.org": true,
}

// normalizeProbeURL validates the input and defaults the scheme to https.
func normalizeProbeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("probe: empty url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("probe: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("probe: unsupported scheme %q (http/https only)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("probe: url has no host")
	}
	return u, nil
}

// Probe inspects rawURL and reports what kind of skill source it is.
func Probe(ctx context.Context, rawURL string) (ProbeReport, error) {
	u, err := normalizeProbeURL(rawURL)
	if err != nil {
		return ProbeReport{}, err
	}
	base := u.Scheme + "://" + u.Host
	rep := ProbeReport{URL: u.String()}

	// 1. Git hosts — no probe traffic needed.
	if gitHosts[strings.ToLower(u.Host)] || strings.HasSuffix(u.Path, ".git") {
		slug := strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, ".git")
		rep.Kind = SourceKindGit
		rep.Detail = fmt.Sprintf(
			"Git repository host. Add a git registry to resolve git-style slugs, "+
				"or install this repo directly: sy skill install %s", strings.TrimRight(slug, "/"))
		rep.Suggested = &config.RegistryConfig{
			ID: registryIDFromHost(u.Host), Type: "git", Priority: 100,
		}
		return rep, nil
	}

	// 2. skills.sh-style directory.
	if samples, ok := probeSkillsSh(ctx, base); ok {
		rep.Kind = SourceKindSkillsSh
		rep.Samples = samples
		rep.HasAudits = true
		rep.Detail = fmt.Sprintf(
			"skills.sh-compatible skill directory (search + inline file trees + partner "+
				"security audits). Install slugs look like owner/repo/skill. Found %d sample skills.",
			len(samples))
		rep.Suggested = &config.RegistryConfig{
			ID: registryIDFromHost(u.Host), Type: "skillssh", BaseURL: base, Priority: 50,
		}
		return rep, nil
	}

	// 3. E19 HTTP registry.
	if samples, ok := probeE19(ctx, base); ok {
		rep.Kind = SourceKindHTTP
		rep.Samples = samples
		rep.Detail = fmt.Sprintf(
			"Soulacy (E19) HTTP package registry. Found %d sample packages. "+
				"If the operator publishes a signing key, set signing_key on the entry.",
			len(samples))
		rep.Suggested = &config.RegistryConfig{
			ID: registryIDFromHost(u.Host), Type: "http", BaseURL: base, Priority: 50,
		}
		return rep, nil
	}

	// 4. Plain page: scrape GitHub repo links as install hints.
	rep.Kind = SourceKindUnknown
	repos := scrapeGitHubRepos(ctx, u.String())
	rep.Samples = repos
	switch {
	case len(repos) > 0:
		rep.Detail = fmt.Sprintf(
			"Not a recognisable registry API, but the page links %d GitHub repos. "+
				"Install one directly: sy skill install %s", len(repos), repos[0])
	default:
		rep.Detail = "Not a recognisable skill source (no skills.sh API, no E19 registry, " +
			"no git host, no repo links found). Check the URL or the site's docs for an API base."
	}
	return rep, nil
}

// probeSkillsSh checks for the skills.sh directory API shape.
func probeSkillsSh(ctx context.Context, base string) (samples []string, ok bool) {
	var body struct {
		Data []skillsShSkill `json:"data"`
	}
	if !getJSONQuick(ctx, base+"/api/v1/skills/search?q=skill&limit=5", &body) {
		// Some deployments may rate-limit search; the leaderboard works too.
		if !getJSONQuick(ctx, base+"/api/v1/skills?per_page=5", &body) {
			return nil, false
		}
	}
	if len(body.Data) == 0 {
		return nil, false
	}
	for _, s := range body.Data {
		if s.ID == "" {
			return nil, false // wrong shape
		}
		samples = append(samples, s.ID)
	}
	return samples, true
}

// probeE19 checks for the E19 registry API shape.
func probeE19(ctx context.Context, base string) (samples []string, ok bool) {
	var body struct {
		Packages []struct {
			Slug string `json:"slug"`
		} `json:"packages"`
	}
	if !getJSONQuick(ctx, base+"/v1/search?q=skill", &body) {
		return nil, false
	}
	if body.Packages == nil {
		return nil, false
	}
	for _, p := range body.Packages {
		if p.Slug != "" {
			samples = append(samples, p.Slug)
		}
	}
	return samples, true
}

func getJSONQuick(ctx context.Context, rawURL string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false
	}
	// Don't require a JSON Content-Type — some servers mislabel; a strict
	// decode of the expected shape is the real test.
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) == nil
}

var githubRepoRe = regexp.MustCompile(`github\.com/([\w.-]+)/([\w.-]+)`)

// scrapeGitHubRepos fetches a page (≤512KiB) and returns deduped
// github.com/owner/repo references, page order, max 10.
func scrapeGitHubRepos(ctx context.Context, pageURL string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range githubRepoRe.FindAllStringSubmatch(string(body), -1) {
		owner, repo := m[1], m[2]
		repo = strings.TrimSuffix(repo, ".git")
		key := strings.ToLower("github.com/" + owner + "/" + repo)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, "github.com/"+owner+"/"+repo)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// registryIDFromHost derives a friendly config id ("skills.sh", "github").
func registryIDFromHost(host string) string {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if host == "github.com" {
		return "github"
	}
	return host
}
