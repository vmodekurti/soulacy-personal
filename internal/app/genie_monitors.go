package app

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

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

	base := m.loader.Get(runtime.GenieAgentID)
	if base == nil {
		return nil, fmt.Errorf("create_monitor: Genie is unavailable")
	}
	def := base.Clone()
	hash := sha256.Sum256([]byte(prompt + "\x00" + cronExpr + "\x00" + atRaw + time.Now().UTC().Format(time.RFC3339Nano)))
	slug := genieSlug(prompt)
	def.ID = fmt.Sprintf("genie-monitor-%s-%x", slug, hash[:3])
	def.Name = "Genie Monitor · " + strings.Title(strings.ReplaceAll(slug, "-", " ")) //nolint:staticcheck -- display-only title casing
	def.Description = prompt
	def.Labels = map[string]string{"soulacy.owner": runtime.GenieAgentID, "soulacy.kind": "monitor"}
	def.SourcePath = ""
	def.Enabled = true
	def.Surfaces = []string{"schedule"}
	def.Channels = nil
	def.SystemPrompt += "\n\nThis is a background monitor. Execute this task: " + prompt + "\nIf its alert condition is not met, return an empty response. If it is met, return only a concise, evidence-backed alert suitable for immediate delivery."
	def.Schedule = &agent.Schedule{}
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

func isGenieMonitor(def *agent.Definition) bool {
	return def != nil && def.Labels["soulacy.owner"] == runtime.GenieAgentID && def.Labels["soulacy.kind"] == "monitor"
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
