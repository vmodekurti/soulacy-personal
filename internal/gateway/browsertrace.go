package gateway

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/browsertrace"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/pkg/agent"
)

type browserPolicySummary struct {
	Enabled          bool     `json:"enabled"`
	Network          string   `json:"network"`
	BrowserAction    string   `json:"browser_action"`
	RequiresApproval bool     `json:"requires_approval"`
	AllowDomains     []string `json:"allow_domains,omitempty"`
	DenyDomains      []string `json:"deny_domains,omitempty"`
	Detail           string   `json:"detail"`
}

// handleBrowserTrace returns the reconstructed browser-automation trace for a
// session: an ordered list of navigate/click/type/extract/screenshot steps plus
// the last URL and screenshot reference. Read-only aggregation over the action
// log plus the effective network/browser policy so operators can tell whether
// navigation is allowed, prompt-gated, or domain restricted before rerunning.
func (s *Server) handleBrowserTrace(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.JSON(fiber.Map{
			"enabled": false,
			"trace":   browsertrace.Trace{Steps: []browsertrace.Step{}},
			"policy":  s.browserTracePolicy(strings.TrimSpace(c.Query("agent_id"))),
		})
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	limit, _ := strconv.Atoi(c.Query("limit", "2000"))
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	events, err := s.actions.Tail(agentID, limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	trace := browsertrace.Build(agentID, sessionID, events)
	s.enrichBrowserTraceScreenshots(&trace)
	return c.JSON(fiber.Map{"enabled": true, "trace": trace, "policy": s.browserTracePolicy(agentID)})
}

func (s *Server) handleBrowserArtifact(c *fiber.Ctx) error {
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging is disabled")
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	ref := strings.TrimSpace(c.Query("path"))
	if agentID == "" || ref == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and path are required")
	}
	events, err := s.actions.Tail(agentID, 5000)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	trace := browsertrace.Build(agentID, sessionID, events)
	if !browserTraceReferencesScreenshot(trace, ref) {
		return s.errMsg(c, fiber.StatusNotFound, "screenshot was not produced by this browser trace")
	}
	path := s.resolveBrowserArtifactPath(ref)
	if path == "" {
		return s.errMsg(c, fiber.StatusNotFound, "screenshot file is not available on disk")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{"error": "screenshot file no longer exists on disk"})
	}
	c.Set(fiber.HeaderContentDisposition, `inline; filename="`+strings.ReplaceAll(filepath.Base(path), `"`, "")+`"`)
	return c.SendFile(path)
}

func (s *Server) enrichBrowserTraceScreenshots(trace *browsertrace.Trace) {
	if trace == nil {
		return
	}
	for i := range trace.Steps {
		ref := strings.TrimSpace(trace.Steps[i].Screenshot)
		if ref == "" || browserScreenshotInline(ref) || s.resolveBrowserArtifactPath(ref) == "" {
			continue
		}
		q := url.Values{}
		q.Set("agent_id", trace.AgentID)
		if strings.TrimSpace(trace.SessionID) != "" {
			q.Set("session_id", trace.SessionID)
		}
		q.Set("path", ref)
		trace.Steps[i].ScreenshotURL = "/api/v1/browser/artifact?" + q.Encode()
	}
}

func browserTraceReferencesScreenshot(trace browsertrace.Trace, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || browserScreenshotInline(ref) {
		return false
	}
	for _, step := range trace.Steps {
		if strings.TrimSpace(step.Screenshot) == ref {
			return true
		}
	}
	return false
}

func browserScreenshotInline(ref string) bool {
	ref = strings.TrimSpace(ref)
	return strings.HasPrefix(ref, "data:image/") || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://")
}

func (s *Server) resolveBrowserArtifactPath(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || browserScreenshotInline(ref) {
		return ""
	}
	candidates := []string{ref}
	if !filepath.IsAbs(ref) {
		if wd, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(wd, ref))
		}
		if ws, err := config.ResolveWorkspace(); err == nil {
			for _, base := range []string{ws.Root, ws.Logs, ws.Agents} {
				if strings.TrimSpace(base) != "" {
					candidates = append(candidates, filepath.Join(base, ref))
				}
			}
		}
	}
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		info, err := os.Stat(clean)
		if err == nil && !info.IsDir() {
			return clean
		}
	}
	return ""
}

func (s *Server) browserTracePolicy(agentID string) browserPolicySummary {
	var def *agent.Definition
	if s != nil && s.loader != nil && strings.TrimSpace(agentID) != "" {
		def = s.loader.Get(strings.TrimSpace(agentID))
	}
	if def == nil || !def.Policy.Enabled {
		return browserPolicySummary{
			Enabled:          false,
			Network:          string(policy.DefaultAction(policy.CategoryNetwork)),
			BrowserAction:    string(policy.DefaultAction(policy.CategoryNetwork)),
			AllowDomains:     nil,
			DenyDomains:      nil,
			RequiresApproval: false,
			Detail:           "No explicit agent policy is configured; browser and network tools use the default network allow behavior.",
		}
	}

	network := effectivePolicyAction(def.Policy.Network, policy.CategoryNetwork)
	summary := browserPolicySummary{
		Enabled:          true,
		Network:          network,
		BrowserAction:    network,
		RequiresApproval: network == string(policy.ActionPrompt),
		AllowDomains:     cloneStrings(def.Policy.AllowDomains),
		DenyDomains:      cloneStrings(def.Policy.DenyDomains),
	}
	switch {
	case network == string(policy.ActionDeny):
		summary.Detail = "Browser navigation is blocked because network tools are denied for this agent."
	case len(summary.AllowDomains) > 0 && len(summary.DenyDomains) > 0:
		summary.Detail = "Browser navigation is limited to allowed domains and excludes denied domains."
	case len(summary.AllowDomains) > 0:
		summary.Detail = "Browser navigation is limited to the configured allow list."
	case len(summary.DenyDomains) > 0:
		summary.Detail = "Browser navigation is allowed except for configured denied domains."
	case network == string(policy.ActionPrompt):
		summary.Detail = "Browser navigation requires approval before network tools run."
	default:
		summary.Detail = "Browser navigation is allowed by this agent's policy."
	}
	return summary
}

func effectivePolicyAction(raw string, cat policy.Category) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(policy.ActionAllow), string(policy.ActionPrompt), string(policy.ActionDeny):
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return string(policy.DefaultAction(cat))
	}
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
