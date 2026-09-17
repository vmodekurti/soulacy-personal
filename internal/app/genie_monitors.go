package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/agentsave"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/pkg/agent"
)

var genieSlugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

type genieMonitorManager struct {
	loader    *runtime.Loader
	scheduler *scheduler.Scheduler
	agentDir  string
}

func (m *genieMonitorManager) CreateGenieMonitor(prompt, cronExpr, atRaw, channel, to string) (map[string]any, error) {
	prompt, cronExpr, atRaw = strings.TrimSpace(prompt), strings.TrimSpace(cronExpr), strings.TrimSpace(atRaw)
	if prompt == "" {
		return nil, fmt.Errorf("create_monitor: prompt is required")
	}
	if (cronExpr == "") == (atRaw == "") {
		return nil, fmt.Errorf("create_monitor: supply exactly one of cron or at")
	}

	hash := sha256.Sum256([]byte(prompt + "\x00" + cronExpr + "\x00" + atRaw + time.Now().UTC().Format(time.RFC3339Nano)))
	slug := genieSlug(prompt)

	// A focused monitor, not a copy of Genie.
	//
	// This used to clone the Genie definition wholesale. The result inherited
	// every one of Genie's twelve tools — including create_monitor, so a
	// scheduled agent running unattended could mint further scheduled agents —
	// along with wildcard access to every skill, peer and MCP server, fifty
	// turns, a thirty-minute budget, and two and a half thousand characters of
	// "you are the master orchestrator" prompt. For something whose whole job
	// is to check one condition and say a sentence, all of that is both
	// wasteful and a wider blast radius than the task needs.
	//
	// So the monitor is built for the job instead. It keeps what a background
	// check genuinely uses: the web, the installed skills, and the ability to
	// hand work to a specialist peer. It does not get to create more of
	// itself, and it does not get to message a channel on its own — the
	// scheduler delivers its output, which is the path the user approved.
	monitorBuiltins := []string{
		"web_search", "list_skills", "read_skill", "read_skill_file",
		"list_mcp_tools", "list_agents",
	}
	mcpServers := []string{"*"}
	def := &agent.Definition{
		ID:          fmt.Sprintf("genie-monitor-%s-%x", slug, hash[:3]),
		Name:        "Genie Monitor · " + strings.Title(strings.ReplaceAll(slug, "-", " ")), //nolint:staticcheck -- display-only title casing
		Description: prompt,
		Labels:      map[string]string{"soulacy.owner": runtime.GenieAgentID, "soulacy.kind": "monitor"},
		Enabled:     true,
		Surfaces:    []string{"schedule"},
		Builtins:    &monitorBuiltins,
		MCPServers:  &mcpServers,
		Skills:      []string{"*"},
		Agents:      []string{"*"},
		// Room to look something up and write a sentence, not to run an
		// open-ended investigation. Fifty turns and half an hour were Genie's
		// budget for interactive work, and a background check that takes that
		// long has gone wrong rather than gone deep.
		MaxTurns:   12,
		RunTimeout: "5m",
		LLM:        agent.LLMConfig{Temperature: 0.2, MaxTokens: 2048},
		Memory:     agent.MemoryPolicy{ReadScopes: []string{"session"}, WriteScopes: []string{"session"}, MaxTokens: 2000},
		Policy:     agent.ToolPolicyConfig{Enabled: true, Shell: "deny", File: "deny", Network: "allow"},
		SystemPrompt: "You are a background monitor. You run on a schedule, unattended, and nobody is waiting to answer a question.\n\n" +
			"Your task: " + prompt + "\n\n" +
			"Check what you need, using a specialist peer agent when one clearly fits. " +
			"If the condition you are watching for is not met, return an empty response — silence is the correct answer to \"nothing happened\". " +
			"If it is met, return only a short, evidence-backed alert, ready to be read as-is. " +
			"Never claim something happened without the evidence in front of you.",
		Schedule: &agent.Schedule{},
	}
	if cronExpr != "" {
		def.Trigger = agent.TriggerCron
		def.Schedule.Cron = cronExpr
	} else {
		at, err := time.Parse(time.RFC3339, atRaw)
		if err != nil {
			return nil, fmt.Errorf("create_monitor: at must be RFC3339: %w", err)
		}
		if !at.After(time.Now()) {
			return nil, fmt.Errorf("create_monitor: at must be in the future")
		}
		def.Trigger = agent.TriggerOneShot
		def.Schedule.At = at
	}
	if strings.TrimSpace(channel) != "" || strings.TrimSpace(to) != "" {
		if strings.TrimSpace(channel) == "" || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("create_monitor: channel and to must be supplied together")
		}
		def.Schedule.Output = &agent.ScheduleOutput{Channel: strings.TrimSpace(channel), To: strings.TrimSpace(to)}
	}
	// The same gate Studio and the builder use. A monitor is written by a
	// model, unattended, which is precisely the case that should not have a
	// weaker path to disk than a person clicking Save.
	decision := agentsave.Gate(context.Background(), def, agentsave.Options{Peer: m.loader.Get})
	switch {
	case decision.Refused != "":
		return nil, fmt.Errorf("create_monitor: %s", decision.Refused)
	case len(decision.Blockers) > 0:
		return nil, fmt.Errorf("create_monitor: %s", decision.Blockers[0].Problem)
	case decision.RequiresConsent:
		// Nobody is present to consent, so the answer is no rather than a
		// silent yes on the user's behalf.
		return nil, fmt.Errorf("create_monitor: this would put a privileged agent on a channel, which needs your approval — build it from the Genie screen instead")
	}

	if err := m.loader.Upsert(m.agentDir, def); err != nil {
		return nil, fmt.Errorf("create_monitor: persist: %w", err)
	}
	if err := m.scheduler.RegisterAgent(def); err != nil {
		_ = m.loader.Delete(def.ID)
		return nil, fmt.Errorf("create_monitor: schedule: %w", err)
	}
	return m.monitorView(def), nil
}

func (m *genieMonitorManager) ListGenieMonitors() []map[string]any {
	entries := map[string]scheduler.ScheduleEntry{}
	for _, entry := range m.scheduler.Entries() {
		entries[entry.AgentID] = entry
	}
	running := m.scheduler.RunningSnapshot()
	out := []map[string]any{}
	for _, def := range m.loader.All() {
		if !isGenieMonitor(def) {
			continue
		}
		view := m.monitorView(def)
		if entry, ok := entries[def.ID]; ok {
			view["next"] = entry.Next
			view["previous"] = entry.Prev
			view["blocked"] = entry.Blocked
			view["blocked_reason"] = entry.BlockedReason
		}
		if started, ok := running[def.ID]; ok {
			view["running"] = true
			view["started_at"] = started
		}
		out = append(out, view)
	}
	return out
}

func (m *genieMonitorManager) PauseGenieMonitor(id string) error {
	def, err := m.owned(id)
	if err != nil {
		return err
	}
	def.Enabled = false
	if err := m.loader.Upsert(m.agentDir, def); err != nil {
		return err
	}
	m.scheduler.DeregisterAgent(id)
	return nil
}

func (m *genieMonitorManager) CancelGenieMonitor(id string) error {
	if _, err := m.owned(id); err != nil {
		return err
	}
	m.scheduler.DeregisterAgent(id)
	return m.loader.Delete(id)
}

func (m *genieMonitorManager) owned(id string) (*agent.Definition, error) {
	def := m.loader.Get(strings.TrimSpace(id))
	if !isGenieMonitor(def) {
		return nil, fmt.Errorf("monitor %q is not owned by Genie", id)
	}
	return def, nil
}

func (m *genieMonitorManager) monitorView(def *agent.Definition) map[string]any {
	v := map[string]any{"id": def.ID, "name": def.Name, "prompt": def.Description, "enabled": def.Enabled, "type": def.Trigger}
	if def.Schedule != nil {
		v["cron"] = def.Schedule.Cron
		if !def.Schedule.At.IsZero() {
			v["at"] = def.Schedule.At
		}
	}
	return v
}

// isGenieMonitor reports whether Genie owns this agent, and therefore whether
// list_monitors, pause_monitor and cancel_monitor may act on it.
//
// It used to require kind "monitor". Genie can now also build a full agent
// through the builder, and an agent the user cannot find again through the
// same conversation that created it is one they cannot stop — so ownership,
// not shape, is the test.
func isGenieMonitor(def *agent.Definition) bool {
	if def == nil || def.Labels["soulacy.owner"] != runtime.GenieAgentID {
		return false
	}
	switch def.Labels["soulacy.kind"] {
	case "monitor", "agent":
		return true
	}
	return false
}

func genieSlug(prompt string) string {
	words := strings.Fields(strings.ToLower(prompt))
	if len(words) > 5 {
		words = words[:5]
	}
	s := strings.Trim(genieSlugCleaner.ReplaceAllString(strings.Join(words, "-"), "-"), "-")
	if s == "" {
		return "task"
	}
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	return s
}
