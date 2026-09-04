package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/pkg/agent"
)

type browserAutomationReadiness struct {
	Status      string                    `json:"status"`
	Score       int                       `json:"score"`
	Ready       int                       `json:"ready"`
	Total       int                       `json:"total"`
	Checks      []browserAutomationCheck  `json:"checks"`
	Sidecars    []browserAutomationServer `json:"sidecars"`
	Policy      browserPolicyPosture      `json:"policy"`
	NextActions []executorAction          `json:"next_actions,omitempty"`
}

type browserAutomationCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Href   string `json:"href,omitempty"`
}

type browserAutomationServer struct {
	ID       string   `json:"id"`
	Mode     string   `json:"mode"`
	Status   string   `json:"status"`
	Command  string   `json:"command,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	Headless bool     `json:"headless"`
	Detail   string   `json:"detail,omitempty"`
}

type browserPolicyPosture struct {
	Status          string   `json:"status"`
	ManagedAgents   int      `json:"managed_agents"`
	UnmanagedAgents int      `json:"unmanaged_agents"`
	UnmanagedIDs    []string `json:"unmanaged_ids,omitempty"`
	Detail          string   `json:"detail"`
}

func (s *Server) handleBrowserStatus(c *fiber.Ctx) error {
	return c.JSON(s.browserAutomationReadiness())
}

func (s *Server) browserAutomationReadiness() browserAutomationReadiness {
	servers := s.browserAutomationServers()
	policyPosture := s.browserPolicyPosture(servers)
	hasSidecar, connectedSidecar, hasHeadless, hasTools := false, false, false, false
	for _, srv := range servers {
		hasSidecar = true
		if srv.Status == "ok" {
			connectedSidecar = true
		}
		if srv.Headless {
			hasHeadless = true
		}
		if len(srv.Tools) > 0 {
			hasTools = true
		}
	}
	actionLog := s != nil && s.actions != nil
	checks := []browserAutomationCheck{
		{
			Key:    "sidecar",
			Label:  "MCP sidecar",
			Status: statusIf(hasSidecar, "ok", "warn"),
			Detail: nextIf(hasSidecar, "A browser-capable MCP server is configured.", "Add the Browser headless MCP quick-start from the MCP page."),
			Href:   "#mcp",
		},
		{
			Key:    "connected",
			Label:  "Connected tools",
			Status: statusIf(connectedSidecar && hasTools, "ok", "warn"),
			Detail: nextIf(connectedSidecar && hasTools, "Browser tools are connected and available to agents.", "Restart the gateway or fix the MCP server until browser tools connect."),
			Href:   "#mcp",
		},
		{
			Key:    "headless",
			Label:  "Headless default",
			Status: statusIf(hasHeadless, "ok", "warn"),
			Detail: nextIf(hasHeadless, "At least one browser sidecar runs headless for scheduled/background agents.", "Use the Browser headless quick-start for unattended agents; keep visible browser only for debugging."),
			Href:   "#mcp",
		},
		{
			Key:    "trace",
			Label:  "Trace replay",
			Status: statusIf(actionLog, "ok", "warn"),
			Detail: nextIf(actionLog, "Action logging can reconstruct browser traces, screenshots, and policy decisions.", "Enable the action log so browser runs can be replayed after failures."),
			Href:   "#browser",
		},
		{
			Key:    "policy",
			Label:  "Domain policy",
			Status: policyPosture.Status,
			Detail: policyPosture.Detail,
			Href:   "#browser",
		},
	}

	ready := 0
	actions := make([]executorAction, 0, 3)
	for _, chk := range checks {
		if chk.Status == "ok" {
			ready++
			continue
		}
		if len(actions) < 3 {
			actions = append(actions, executorAction{Label: chk.Label, Detail: chk.Detail, Href: chk.Href})
		}
	}
	score := int(float64(ready) / float64(len(checks)) * 100)
	status := "ok"
	if score < 80 {
		status = "warn"
	}
	if ready == 0 {
		status = "fail"
	}
	return browserAutomationReadiness{
		Status:      status,
		Score:       score,
		Ready:       ready,
		Total:       len(checks),
		Checks:      checks,
		Sidecars:    servers,
		Policy:      policyPosture,
		NextActions: actions,
	}
}

func (s *Server) browserPolicyPosture(servers []browserAutomationServer) browserPolicyPosture {
	if s == nil || s.loader == nil {
		return browserPolicyPosture{Status: "warn", Detail: "Agent loader is unavailable, so browser policy coverage could not be verified."}
	}
	hasBrowserSidecar := len(servers) > 0
	managed, unmanaged := 0, make([]string, 0)
	for _, def := range s.loader.All() {
		if def == nil || !agentUsesBrowserAutomation(def, hasBrowserSidecar) {
			continue
		}
		if hasExplicitBrowserPolicy(def) {
			managed++
		} else {
			unmanaged = append(unmanaged, def.ID)
		}
	}
	if len(unmanaged) == 0 {
		if managed == 0 {
			return browserPolicyPosture{
				Status: "ok",
				Detail: "No browser-capable agents are currently detected; future browser agents should declare network/domain policy.",
			}
		}
		return browserPolicyPosture{
			Status:        "ok",
			ManagedAgents: managed,
			Detail:        "All detected browser-capable agents declare explicit network/domain policy.",
		}
	}
	ids := unmanaged
	if len(ids) > 6 {
		ids = ids[:6]
	}
	return browserPolicyPosture{
		Status:          "warn",
		ManagedAgents:   managed,
		UnmanagedAgents: len(unmanaged),
		UnmanagedIDs:    ids,
		Detail:          "Some browser-capable agents can navigate without explicit allow/deny/prompt policy. Add policy.network plus allow_domains or deny_domains before production.",
	}
}

func (s *Server) browserAutomationServers() []browserAutomationServer {
	if s == nil || s.mcp == nil {
		return nil
	}
	snap := s.mcp.ServersSnapshot()
	out := make([]browserAutomationServer, 0, len(snap))
	for _, srv := range snap {
		if !looksLikeBrowserServer(srv) {
			continue
		}
		tools := browserToolNames(srv.Tools)
		status := "warn"
		if srv.Connected && len(tools) > 0 {
			status = "ok"
		}
		out = append(out, browserAutomationServer{
			ID:       srv.ID,
			Mode:     browserServerMode(srv),
			Status:   status,
			Command:  strings.TrimSpace(srv.Command + " " + strings.Join(srv.Args, " ")),
			Tools:    tools,
			Headless: browserServerHeadless(srv),
			Detail:   srv.Detail,
		})
	}
	return out
}

func looksLikeBrowserServer(srv mcp.ServerStatus) bool {
	hay := strings.ToLower(strings.Join(append([]string{srv.ID, srv.Command, srv.URL}, srv.Args...), " "))
	if strings.Contains(hay, "playwright") || strings.Contains(hay, "puppeteer") || strings.Contains(hay, "browser") {
		return true
	}
	return len(browserToolNames(srv.Tools)) > 0
}

func browserToolNames(tools []mcp.ToolSummary) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		hay := strings.ToLower(t.Name + " " + t.FullName + " " + t.Description)
		if strings.Contains(hay, "browser") || strings.Contains(hay, "page") || strings.Contains(hay, "screenshot") || strings.Contains(hay, "navigate") {
			out = append(out, t.FullName)
		}
	}
	if len(out) > 8 {
		return out[:8]
	}
	return out
}

func browserServerHeadless(srv mcp.ServerStatus) bool {
	hay := strings.ToLower(strings.Join(append([]string{srv.ID, srv.Command}, srv.Args...), " "))
	return strings.Contains(hay, "--headless") || strings.Contains(hay, "headless")
}

func browserServerMode(srv mcp.ServerStatus) string {
	if browserServerHeadless(srv) {
		return "headless"
	}
	if srv.Transport == "http" || srv.URL != "" {
		return "remote"
	}
	return "visible"
}

func agentUsesBrowserAutomation(def *agent.Definition, hasBrowserSidecar bool) bool {
	if def == nil {
		return false
	}
	if strPtrHasBrowser(def.MCPServers) || strPtrHasBrowser(def.MCPTools) {
		return true
	}
	if strPtrHasWildcard(def.MCPServers) || strPtrHasWildcard(def.MCPTools) {
		return hasBrowserSidecar
	}
	for _, t := range def.Tools {
		hay := strings.ToLower(t.Name + " " + t.Description)
		if strings.Contains(hay, "browser") || strings.Contains(hay, "playwright") || strings.Contains(hay, "puppeteer") {
			return true
		}
	}
	return false
}

func hasExplicitBrowserPolicy(def *agent.Definition) bool {
	if def == nil || !def.Policy.Enabled {
		return false
	}
	return strings.TrimSpace(def.Policy.Network) != "" ||
		len(def.Policy.AllowDomains) > 0 ||
		len(def.Policy.DenyDomains) > 0
}

func strPtrHasBrowser(values *[]string) bool {
	if values == nil {
		return false
	}
	for _, v := range *values {
		hay := strings.ToLower(strings.TrimSpace(v))
		if strings.Contains(hay, "browser") || strings.Contains(hay, "playwright") || strings.Contains(hay, "puppeteer") || strings.Contains(hay, "screenshot") || strings.Contains(hay, "navigate") {
			return true
		}
	}
	return false
}

func strPtrHasWildcard(values *[]string) bool {
	if values == nil {
		return false
	}
	for _, v := range *values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "*", "all":
			return true
		}
	}
	return false
}

func parityBrowserAutomation(b browserAutomationReadiness) parityArea {
	if b.Status == "ok" {
		return parityArea{Key: "browser", Label: "Browser Automation", Status: "ok", Score: maxInt(b.Score, 82), Detail: "Browser automation has MCP sidecar readiness, headless setup, policy visibility, and trace replay.", Next: "Add live session viewing and reusable browser skills for the last mile of parity.", Benchmark: "OpenClaw/Hermes", Href: "#browser"}
	}
	if b.Score >= 60 {
		return parityArea{Key: "browser", Label: "Browser Automation", Status: "warn", Score: b.Score, Detail: "Browser automation is partially ready, but one or more sidecar, headless, connection, or trace checks need attention.", Next: firstBrowserNextAction(b), Benchmark: "OpenClaw/Hermes", Href: "#browser"}
	}
	return parityArea{Key: "browser", Label: "Browser Automation", Status: "fail", Score: maxInt(b.Score, 30), Detail: "Browser automation is not production-ready yet.", Next: firstBrowserNextAction(b), Benchmark: "OpenClaw/Hermes", Href: "#mcp"}
}

func firstBrowserNextAction(b browserAutomationReadiness) string {
	if len(b.NextActions) > 0 && strings.TrimSpace(b.NextActions[0].Detail) != "" {
		return b.NextActions[0].Detail
	}
	return "Add Browser headless from the MCP page, restart, and verify a trace appears on the Browser page."
}
