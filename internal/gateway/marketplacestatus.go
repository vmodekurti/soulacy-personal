package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
)

type marketplaceReadiness struct {
	Status            string                       `json:"status"`
	Score             int                          `json:"score"`
	Ready             int                          `json:"ready"`
	Total             int                          `json:"total"`
	InstalledSkills   int                          `json:"installed_skills"`
	Registries        int                          `json:"registries"`
	DefaultRegistries bool                         `json:"default_registries"`
	SearchableSources int                          `json:"searchable_sources"`
	SignedSources     int                          `json:"signed_sources"`
	AuthedSources     int                          `json:"authed_sources"`
	ProviderTypes     []string                     `json:"provider_types"`
	Checks            []marketplaceReadinessCheck  `json:"checks"`
	Sources           []marketplaceReadinessSource `json:"sources"`
	NextActions       []string                     `json:"next_actions"`
}

type marketplaceReadinessCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type marketplaceReadinessSource struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	BaseURL    string `json:"base_url,omitempty"`
	Priority   int    `json:"priority"`
	HasAuth    bool   `json:"has_auth"`
	Signed     bool   `json:"signed"`
	Searchable bool   `json:"searchable"`
}

func (s *Server) handleMarketplaceStatus(c *fiber.Ctx) error {
	return c.JSON(s.marketplaceReadiness())
}

func (s *Server) marketplaceReadiness() marketplaceReadiness {
	// H1 — nil-safe front. The readiness surface is exercised by tests that
	// construct a zero-value *Server; guarding here means the empty-config
	// path returns a valid empty readiness rather than panicking on s.cfg
	// or s.log deref inside configuredOrDefaultRegistries / FromConfig.
	if s == nil {
		return marketplaceReadiness{
			Status:      "warn",
			NextActions: []string{"server not initialised"},
		}
	}
	regs := s.configuredOrDefaultRegistries()
	defaults := len(regs) == len(pkgregistry.DefaultRegistries())
	if defaults {
		defs := pkgregistry.DefaultRegistries()
		for i := range regs {
			if regs[i].ID != defs[i].ID || regs[i].Type != defs[i].Type || regs[i].BaseURL != defs[i].BaseURL {
				defaults = false
				break
			}
		}
	}
	_, registryWarnings := pkgregistry.FromConfig(regs, s.log)

	sources := make([]marketplaceReadinessSource, 0, len(regs))
	searchable, signed, authed := 0, 0, 0
	for _, r := range regs {
		typ := strings.TrimSpace(r.Type)
		if typ == "" {
			typ = "http"
		}
		src := marketplaceReadinessSource{
			ID:         strings.TrimSpace(r.ID),
			Type:       typ,
			BaseURL:    strings.TrimSpace(r.BaseURL),
			Priority:   r.Priority,
			HasAuth:    len(r.AuthHeaders) > 0,
			Signed:     strings.TrimSpace(r.SigningKey) != "",
			Searchable: typ == "http" || typ == "skillssh",
		}
		if src.ID == "" {
			src.ID = typ
		}
		if src.Searchable {
			searchable++
		}
		if src.Signed {
			signed++
		}
		if src.HasAuth {
			authed++
		}
		sources = append(sources, src)
	}

	// H6 — s is guaranteed non-nil by the H1 nil-guard at the top of this
	// function. The prior `s != nil` checks here were dead defense that
	// tripped staticcheck's SA5011 inference (it saw the later nil-check
	// and inferred earlier deref sites might be reading a nil). Simplified
	// to skillLoader-only checks now that the invariant holds.
	installed := 0
	if s.skillLoader != nil {
		installed = len(s.skillLoader.All())
	}

	checks := []marketplaceReadinessCheck{
		marketplaceCheck("loader", "Skill loader", s.skillLoader != nil,
			"Skill loader is available for installed and newly added skills.",
			"Skill loader is unavailable; install/rescan flows cannot hot-load skills."),
		marketplaceCheck("installed_skills", "Installed skills", installed > 0,
			plural(installed, "installed skill")+" available to agents.",
			"No installed skills found. Install at least one launch template or domain skill."),
		marketplaceCheck("sources", "Registry sources", len(regs) > 0,
			plural(len(regs), "registry source")+" configured or active by default.",
			"No package registry sources are available."),
		marketplaceCheck("search", "Searchable catalog", searchable > 0,
			plural(searchable, "searchable source")+" can power GUI discovery.",
			"Add an HTTP or skills.sh-compatible registry for GUI discovery."),
		marketplaceCheck("safety", "Install safety pipeline", true,
			"Registry installs pass through local safety introspection before activation.",
			"Safety pipeline is not wired."),
		marketplaceCheck("signatures", "Signature trust", signed > 0,
			plural(signed, "source")+" enforce package signatures.",
			"No registry signing key configured; unverified installs require explicit operator opt-in."),
		marketplaceCheck("providers", "Registry providers", len(registry.PkgRegistries()) > 0,
			"Registered package providers: "+strings.Join(registry.PkgRegistries(), ", ")+".",
			"No package registry providers are registered."),
	}
	if len(registryWarnings) > 0 {
		checks = append(checks, marketplaceReadinessCheck{
			Key:    "source_warnings",
			Label:  "Source configuration",
			Status: "warn",
			Detail: registryWarnings[0].Error(),
		})
	}

	ready, total := 0, len(checks)
	for _, check := range checks {
		if check.Status == "ok" {
			ready++
		}
	}
	score := 0
	if total > 0 {
		for _, check := range checks {
			switch check.Status {
			case "ok":
				score += 100
			case "warn":
				score += 55
			}
		}
		score /= total
	}
	status := statusIf(score >= 76, "ok", statusIf(score >= 45, "warn", "fail"))
	next := marketplaceNextActions(installed, len(regs), searchable, signed, len(registryWarnings), defaults)

	return marketplaceReadiness{
		Status:            status,
		Score:             score,
		Ready:             ready,
		Total:             total,
		InstalledSkills:   installed,
		Registries:        len(regs),
		DefaultRegistries: defaults,
		SearchableSources: searchable,
		SignedSources:     signed,
		AuthedSources:     authed,
		ProviderTypes:     registry.PkgRegistries(),
		Checks:            checks,
		Sources:           sources,
		NextActions:       next,
	}
}

func marketplaceCheck(key, label string, ok bool, okDetail, failDetail string) marketplaceReadinessCheck {
	if ok {
		return marketplaceReadinessCheck{Key: key, Label: label, Status: "ok", Detail: okDetail}
	}
	return marketplaceReadinessCheck{Key: key, Label: label, Status: "warn", Detail: failDetail}
}

func marketplaceNextActions(installed, registries, searchable, signed, warnings int, defaults bool) []string {
	var out []string
	if installed == 0 {
		out = append(out, "Install at least one verified starter skill so new agents have reusable guidance.")
	}
	if registries == 0 || searchable == 0 {
		out = append(out, "Add a searchable skills.sh or Soulacy HTTP package registry source.")
	}
	if signed == 0 {
		out = append(out, "Configure signing_key for trusted registries before promoting one-click installs to production.")
	}
	if warnings > 0 {
		out = append(out, "Open Skills -> Skill sources and fix registry warnings or authentication headers.")
	}
	if defaults {
		out = append(out, "Keep the built-in skills.sh and git defaults, or pin explicit enterprise registries in config.yaml.")
	}
	if len(out) == 0 {
		out = append(out, "Promote verified skill packs with changelogs and compatibility notes.")
	}
	return out
}

func parityMarketplace(m marketplaceReadiness) parityArea {
	detail := plural(m.InstalledSkills, "installed skill") + ", " + plural(m.Registries, "registry source") + ", " + plural(m.SearchableSources, "searchable source") + "."
	if m.SignedSources > 0 {
		detail += " Signed source trust is configured."
	} else {
		detail += " Signed source trust is not configured yet."
	}
	next := "Promote verified skill packs with changelogs, compatibility checks, and safer one-click install flows."
	if len(m.NextActions) > 0 {
		next = m.NextActions[0]
	}
	return parityArea{
		Key:       "marketplace",
		Label:     "Marketplace Ecosystem",
		Status:    m.Status,
		Score:     maxInt(m.Score, statusIfInt(m.Status == "ok", 76, 0)),
		Detail:    detail,
		Next:      next,
		Benchmark: "OpenClaw/Hermes",
		Href:      "#skills",
	}
}

func statusIfInt(ok bool, yes, no int) int {
	if ok {
		return yes
	}
	return no
}
