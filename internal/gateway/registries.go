// registries.go — Story E26: review URLs as skill sources and manage the
// `registries:` config block from the GUI.
//
//	GET  /api/v1/registries        — configured sources (auth headers redacted)
//	POST /api/v1/registries/probe  — {url} → pkgregistry.ProbeReport
//	POST /api/v1/registries        — add a reviewed source to config.yaml
//
// Probe targets are operator-supplied (config-write RBAC) and restricted to
// http/https by pkgregistry.Probe.
package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/pkg/skill"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
)

// registryView is the redacted listing shape.
type registryView struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	BaseURL    string `json:"base_url,omitempty"`
	Priority   int    `json:"priority"`
	HasAuth    bool   `json:"has_auth"`
	SigningKey string `json:"signing_key,omitempty"`
}

// rawRegistries reads the registries block from config.yaml as raw maps.
func (s *Server) rawRegistries() ([]map[string]any, error) {
	current, err := readRawConfig(s.cfgPath)
	if err != nil {
		return nil, err
	}
	raw, _ := current["registries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// configuredOrDefaultRegistries mirrors the CLI's behaviour: with no
// registries: block, the native defaults (skills.sh + git) apply. With an
// explicit registries: block, keep the operator's searchable sources but retain
// the safe git resolver as an addressable fallback so direct GitHub/source URL
// installs keep working from the GUI.
func (s *Server) configuredOrDefaultRegistries() []config.RegistryConfig {
	var regs []config.RegistryConfig
	if s.cfgPath != "" {
		if raw, err := s.rawRegistries(); err == nil {
			for _, m := range raw {
				regs = append(regs, config.RegistryConfig{
					ID:          strAt(m, "id"),
					Type:        strAt(m, "type"),
					BaseURL:     strAt(m, "base_url"),
					Priority:    intAt(m, "priority"),
					AuthHeaders: stringMapAt(m, "auth_headers"),
					SigningKey:  strAt(m, "signing_key"),
				})
			}
		}
	}
	if len(regs) == 0 {
		return pkgregistry.DefaultRegistries()
	}
	if !hasRegistryType(regs, "git") {
		regs = append(regs, config.RegistryConfig{ID: "git", Type: "git", Priority: fallbackRegistryPriority(regs)})
	}
	return regs
}

func hasRegistryType(regs []config.RegistryConfig, typ string) bool {
	for _, r := range regs {
		if strings.EqualFold(strings.TrimSpace(r.Type), typ) || (strings.TrimSpace(r.Type) == "" && typ == "http") {
			return true
		}
	}
	return false
}

func fallbackRegistryPriority(regs []config.RegistryConfig) int {
	max := 100
	for _, r := range regs {
		if r.Priority > max {
			max = r.Priority
		}
	}
	return max + 100
}

// handleSearchRegistries searches every configured (or default) skill
// source — natively including skills.sh — for the GUI's skill finder.
func (s *Server) handleSearchRegistries(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "q query parameter is required")
	}
	eng, _ := pkgregistry.FromConfig(s.configuredOrDefaultRegistries(), s.log)
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
	defer cancel()
	pkgs, warnings := eng.SearchDetailed(ctx, query)
	pkgs = appendLocalSkillMatches(pkgs, s.localSkillMatches(query))
	warnText := make([]string, 0, len(warnings))
	authRequired := false
	for _, w := range warnings {
		if w != nil {
			msg := w.Error()
			warnText = append(warnText, msg)
			low := strings.ToLower(msg)
			if strings.Contains(low, "401") || strings.Contains(low, "authentication") || strings.Contains(low, "unauthorized") {
				authRequired = true
			}
		}
	}
	suggestions := []string{}
	if authRequired {
		suggestions = append(suggestions,
			"One or more skill sources require authentication. Add auth_headers for that registry or set the required environment token before restarting Soulacy.",
			"If you already know the skill repository, install it directly with `sy skill install <github-url-or-slug>`.",
		)
	}
	status := "ok"
	if len(warnText) > 0 {
		status = "degraded"
	}
	return c.JSON(fiber.Map{
		"packages":      pkgs,
		"count":         len(pkgs),
		"warnings":      warnText,
		"status":        status,
		"checked":       eng.Providers(),
		"auth_required": authRequired,
		"suggestions":   suggestions,
	})
}

func (s *Server) localSkillMatches(query string) []sdkpkg.Package {
	if s.skillLoader == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var skills []*skill.Skill
	if l, ok := s.skillLoader.(interface{ All() []*skill.Skill }); ok {
		skills = l.All()
	}
	out := make([]sdkpkg.Package, 0)
	for _, sk := range skills {
		if sk == nil {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{sk.Name, sk.Description}, " "))
		if !strings.Contains(haystack, q) && !strings.Contains(normalizeSearchText(haystack), normalizeSearchText(q)) {
			continue
		}
		out = append(out, sdkpkg.Package{
			Slug:        sk.Name,
			Version:     "installed",
			Description: sk.Description,
			Provider:    "local",
			Manifest: map[string]any{
				"installed": true,
				"path":      sk.Path,
			},
		})
	}
	return out
}

func appendLocalSkillMatches(remote, local []sdkpkg.Package) []sdkpkg.Package {
	if len(local) == 0 {
		return remote
	}
	seen := map[string]bool{}
	out := make([]sdkpkg.Package, 0, len(remote)+len(local))
	for _, pkg := range remote {
		seen[strings.ToLower(pkg.Slug)] = true
		out = append(out, pkg)
	}
	for _, pkg := range local {
		if seen[strings.ToLower(pkg.Slug)] {
			continue
		}
		out = append(out, pkg)
	}
	return out
}

func normalizeSearchText(s string) string {
	replacer := strings.NewReplacer("-", "", "_", "", " ", "", ".", "")
	return replacer.Replace(strings.ToLower(s))
}

func (s *Server) handleListRegistries(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.JSON(fiber.Map{"registries": []registryView{}, "count": 0})
	}
	entries, err := s.rawRegistries()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if len(entries) == 0 {
		// Surface the native defaults so the GUI shows what's active.
		views := []registryView{}
		for _, d := range pkgregistry.DefaultRegistries() {
			views = append(views, registryView{ID: d.ID, Type: d.Type, BaseURL: d.BaseURL, Priority: d.Priority})
		}
		return c.JSON(fiber.Map{"registries": views, "count": len(views), "defaults": true})
	}
	views := make([]registryView, 0, len(entries))
	for _, m := range entries {
		v := registryView{
			ID:       strAt(m, "id"),
			Type:     strAt(m, "type"),
			BaseURL:  strAt(m, "base_url"),
			Priority: intAt(m, "priority"),
		}
		if v.Type == "" {
			v.Type = "http"
		}
		if ah, ok := m["auth_headers"].(map[string]any); ok && len(ah) > 0 {
			v.HasAuth = true
		}
		if sk := strAt(m, "signing_key"); sk != "" {
			v.SigningKey = sk // public key — not a secret
		}
		views = append(views, v)
	}
	return c.JSON(fiber.Map{"registries": views, "count": len(views)})
}

func (s *Server) handleProbeRegistry(c *fiber.Ctx) error {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "body must be {\"url\": \"…\"}")
	}
	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()
	rep, err := pkgregistry.Probe(ctx, body.URL)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(rep)
}

func (s *Server) handleAddRegistry(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — restart with SOULACY_CONFIG_PATH set to enable writes",
		})
	}
	var body struct {
		ID          string            `json:"id"`
		Type        string            `json:"type"`
		BaseURL     string            `json:"base_url"`
		Priority    int               `json:"priority"`
		SigningKey  string            `json:"signing_key"`
		AuthHeaders map[string]string `json:"auth_headers"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if body.Type == "" {
		body.Type = "http"
	}
	if !pkgRegistryTypeKnown(body.Type) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown registry type \"" + body.Type + "\" (registered: " + strings.Join(registry.PkgRegistries(), ", ") + ")",
		})
	}
	if body.Type != "git" && strings.TrimSpace(body.BaseURL) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "base_url is required for type "+body.Type)
	}
	if body.ID == "" {
		body.ID = body.Type
	}

	current, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	raw, _ := current["registries"].([]any)
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok && strAt(m, "id") == body.ID {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "a registry with id \"" + body.ID + "\" already exists",
			})
		}
	}
	entry := map[string]any{"id": body.ID, "type": body.Type}
	if body.BaseURL != "" {
		entry["base_url"] = strings.TrimRight(body.BaseURL, "/")
	}
	if body.Priority != 0 {
		entry["priority"] = body.Priority
	}
	if body.SigningKey != "" {
		entry["signing_key"] = body.SigningKey
	}
	if len(body.AuthHeaders) > 0 {
		headers := map[string]any{}
		for k, v := range body.AuthHeaders {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			headers[k] = v
		}
		if len(headers) > 0 {
			entry["auth_headers"] = headers
		}
	}
	current["registries"] = append(raw, entry)
	if err := writeRawConfig(s.cfgPath, current); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("registry source added via API",
		zap.String("id", body.ID), zap.String("type", body.Type), zap.String("base_url", body.BaseURL))
	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Source \"" + body.ID + "\" saved. `sy skill install` picks it up immediately; restart the gateway for GUI installs.",
	})
}

func pkgRegistryTypeKnown(t string) bool {
	for _, n := range registry.PkgRegistries() {
		if n == t {
			return true
		}
	}
	return false
}

func strAt(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func intAt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func stringMapAt(m map[string]any, k string) map[string]string {
	raw, ok := m[k]
	if !ok {
		return nil
	}
	out := map[string]string{}
	switch v := raw.(type) {
	case map[string]string:
		for kk, vv := range v {
			out[kk] = vv
		}
	case map[string]any:
		for kk, vv := range v {
			if s, ok := vv.(string); ok {
				out[kk] = s
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
