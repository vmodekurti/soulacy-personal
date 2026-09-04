package gateway

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/pkg/message"
)

type runLedgerRow struct {
	RunID           string    `json:"runId"`
	ID              string    `json:"id"`
	AgentID         string    `json:"agentId,omitempty"`
	AgentName       string    `json:"agentName,omitempty"`
	SessionID       string    `json:"sessionId,omitempty"`
	Trigger         string    `json:"trigger,omitempty"`
	Channel         string    `json:"channel,omitempty"`
	Source          string    `json:"source,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
	Steps           int       `json:"steps,omitempty"`
	Ok              bool      `json:"ok"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	Output          string    `json:"output,omitempty"`
	DeliveryChannel string    `json:"deliveryChannel,omitempty"`
	DeliveryTo      string    `json:"deliveryTo,omitempty"`
	DeliveryStatus  string    `json:"deliveryStatus,omitempty"`
	DeliveryError   string    `json:"deliveryError,omitempty"`
	HasBrowserTrace bool      `json:"hasBrowserTrace,omitempty"`
	BrowserEvents   int       `json:"browserEvents,omitempty"`
	EventCount      int       `json:"eventCount,omitempty"`
	DurationMS      int64     `json:"durationMs,omitempty"`
}

// handleRunLedger exposes one durable run ledger for Schedule, Activity, and
// support screens. It groups the action log into executions across all trigger
// paths: manual/http runs, chat/channel messages, cron runs, and delivery-only
// schedule.output records. The goal is that a run appears in one place even
// when the UI that initiated it is not the UI that later inspects it.
func (s *Server) handleRunLedger(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	eventLimit := c.QueryInt("event_limit", 10000)
	if eventLimit <= 0 {
		eventLimit = 10000
	}
	if eventLimit > 50000 {
		eventLimit = 50000
	}

	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	var (
		events    []message.Event
		rows      []runLedgerRow
		sources   []string
		queryNote string
	)
	if s.actions != nil {
		q, ok := s.actions.(eventQuerier)
		if !ok {
			queryNote = "action log backend does not support durable event queries"
		} else {
			got, err := q.QueryEvents(agentID, sessionID, eventLimit, runLedgerEventTypes())
			if err != nil {
				return s.errJSON(c, fiber.StatusInternalServerError, err)
			}
			events = got
			rows = append(rows, s.buildRunLedger(events, 0)...)
			sources = append(sources, "action-log")
		}
	}
	if sessionID == "" {
		flowRows := s.flowRunLedgerRows(agentID)
		if len(flowRows) > 0 {
			rows = append(rows, flowRows...)
			sources = append(sources, "flow")
		}
	}
	if len(sources) == 0 {
		status := fiber.StatusServiceUnavailable
		msg := "run ledger not available (no action log or flow history)"
		if queryNote != "" {
			msg = queryNote
		}
		return c.Status(status).JSON(fiber.Map{"error": msg})
	}
	rows = mergeRunLedgerRows(rows, limit)
	return c.JSON(fiber.Map{
		"agent_id":        agentID,
		"session_id":      sessionID,
		"runs":            rows,
		"count":           len(rows),
		"event_count":     len(events),
		"event_limit":     eventLimit,
		"event_truncated": len(events) >= eventLimit,
		"durable":         runLedgerContainsString(sources, "action-log"),
		"source":          strings.Join(studioUniqueStrings(sources), "+"),
	})
}

func runLedgerEventTypes() map[string]bool {
	return map[string]bool{
		"message.in":       true,
		"message.out":      true,
		"error":            true,
		"tool.call":        true,
		"tool.result":      true,
		"reasoning.step":   true,
		"reasoning.result": true,
		"schedule.output":  true,
	}
}

func (s *Server) buildRunLedger(events []message.Event, limit int) []runLedgerRow {
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })

	agentNames := s.runLedgerAgentNames()
	byRun := map[string][]message.Event{}
	runAgent := map[string]string{}
	runSession := map[string]string{}
	currentByAgentSession := map[string]string{}
	startsByAgentSession := map[string]int{}
	order := []string{}

	for i, ev := range events {
		if ev.Type == "" {
			continue
		}
		agentID := strings.TrimSpace(ev.AgentID)
		if agentID == "" {
			agentID = "unknown"
		}
		sessionID := strings.TrimSpace(ev.SessionID)
		if sessionID == "" {
			sessionID = fmt.Sprintf("event-%d", i)
		}
		key := agentID + "\x00" + sessionID
		runID := currentByAgentSession[key]
		if ev.Type == "message.in" || runID == "" {
			if startsByAgentSession[key] == 0 {
				runID = sessionID
			} else {
				runID = durableHistoryRunID(sessionID, ev.Timestamp, i)
			}
			startsByAgentSession[key]++
			currentByAgentSession[key] = runID
			order = append(order, runID)
		}
		if _, ok := byRun[runID]; !ok {
			runAgent[runID] = agentID
			runSession[runID] = sessionID
		}
		byRun[runID] = append(byRun[runID], ev)
	}

	rows := make([]runLedgerRow, 0, len(byRun))
	seen := map[string]bool{}
	for _, runID := range order {
		if seen[runID] {
			continue
		}
		seen[runID] = true
		base, ok := summarizeActionEvents(runID, runSession[runID], byRun[runID])
		if !ok {
			continue
		}
		agentID := runAgent[runID]
		row := runLedgerRow{
			RunID:           base.RunID,
			ID:              base.RunID,
			AgentID:         agentID,
			AgentName:       studioFirstNonEmpty(agentNames[agentID], agentID),
			SessionID:       base.SessionID,
			Trigger:         base.Trigger,
			Channel:         base.Trigger,
			Source:          "action-log",
			StartedAt:       base.StartedAt,
			UpdatedAt:       base.UpdatedAt,
			Steps:           base.Steps,
			Ok:              base.Ok,
			Status:          base.Status,
			Error:           base.Error,
			Output:          base.Output,
			DeliveryChannel: base.DeliveryChannel,
			DeliveryTo:      base.DeliveryTo,
			DeliveryStatus:  base.DeliveryStatus,
			DeliveryError:   base.DeliveryError,
			HasBrowserTrace: runLedgerHasBrowserTrace(byRun[runID]),
			BrowserEvents:   runLedgerBrowserEventCount(byRun[runID]),
			EventCount:      len(byRun[runID]),
		}
		if row.UpdatedAt.IsZero() {
			row.UpdatedAt = row.StartedAt
		}
		if !row.StartedAt.IsZero() && !row.UpdatedAt.IsZero() {
			row.DurationMS = row.UpdatedAt.Sub(row.StartedAt).Milliseconds()
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (s *Server) runLedgerAgentNames() map[string]string {
	out := map[string]string{}
	if s == nil || s.loader == nil {
		return out
	}
	for _, def := range s.loader.All() {
		if def != nil {
			out[def.ID] = def.Name
		}
	}
	return out
}

func (s *Server) flowRunLedgerRows(agentID string) []runLedgerRow {
	if s == nil || s.engine == nil {
		return nil
	}
	agentNames := s.runLedgerAgentNames()
	ids := []string{}
	if strings.TrimSpace(agentID) != "" {
		ids = append(ids, strings.TrimSpace(agentID))
	} else {
		for id := range agentNames {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	rows := []runLedgerRow{}
	for _, id := range ids {
		for _, r := range s.engine.FlowRunHistory(id) {
			status := "success"
			if !r.Ok {
				status = "failed"
			}
			row := runLedgerRow{
				RunID:     r.RunID,
				ID:        r.RunID,
				AgentID:   id,
				AgentName: studioFirstNonEmpty(agentNames[id], id),
				SessionID: r.RunID,
				Trigger:   r.Trigger,
				Channel:   r.Trigger,
				Source:    "flow",
				StartedAt: r.StartedAt,
				UpdatedAt: r.UpdatedAt,
				Steps:     r.Steps,
				Ok:        r.Ok,
				Status:    status,
				Error:     r.Error,
			}
			if row.UpdatedAt.IsZero() {
				row.UpdatedAt = row.StartedAt
			}
			if row.Error != "" {
				row.Output = row.Error
			}
			if !row.StartedAt.IsZero() && !row.UpdatedAt.IsZero() {
				row.DurationMS = row.UpdatedAt.Sub(row.StartedAt).Milliseconds()
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func mergeRunLedgerRows(rows []runLedgerRow, limit int) []runLedgerRow {
	byKey := map[string]runLedgerRow{}
	for _, row := range rows {
		key := row.AgentID + "\x00" + row.RunID
		if cur, ok := byKey[key]; ok {
			byKey[key] = mergeRunLedgerRow(cur, row)
		} else {
			byKey[key] = row
		}
	}
	out := make([]runLedgerRow, 0, len(byKey))
	for _, row := range byKey {
		if row.ID == "" {
			row.ID = row.RunID
		}
		if row.UpdatedAt.IsZero() {
			row.UpdatedAt = row.StartedAt
		}
		if row.Status == "" {
			row.Status = "unknown"
		}
		if !row.StartedAt.IsZero() && !row.UpdatedAt.IsZero() {
			row.DurationMS = row.UpdatedAt.Sub(row.StartedAt).Milliseconds()
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func mergeRunLedgerRow(a, b runLedgerRow) runLedgerRow {
	out := a
	out.ID = studioFirstNonEmpty(out.ID, b.ID)
	out.RunID = studioFirstNonEmpty(out.RunID, b.RunID)
	out.AgentID = studioFirstNonEmpty(out.AgentID, b.AgentID)
	out.AgentName = studioFirstNonEmpty(out.AgentName, b.AgentName)
	out.SessionID = studioFirstNonEmpty(out.SessionID, b.SessionID)
	out.Trigger = studioFirstNonEmpty(out.Trigger, b.Trigger)
	out.Channel = studioFirstNonEmpty(out.Channel, b.Channel, out.Trigger)
	out.Source = strings.Join(studioUniqueStrings([]string{out.Source, b.Source}), "+")
	out.StartedAt = studioFirstNonZeroTime(out.StartedAt, b.StartedAt)
	if !b.StartedAt.IsZero() && (out.StartedAt.IsZero() || b.StartedAt.Before(out.StartedAt)) {
		out.StartedAt = b.StartedAt
	}
	if b.UpdatedAt.After(out.UpdatedAt) {
		out.UpdatedAt = b.UpdatedAt
	}
	if b.Steps > out.Steps {
		out.Steps = b.Steps
	}
	out.Output = studioFirstNonEmpty(out.Output, b.Output)
	out.Error = studioFirstNonEmpty(out.Error, b.Error)
	out.DeliveryChannel = studioFirstNonEmpty(out.DeliveryChannel, b.DeliveryChannel)
	out.DeliveryTo = studioFirstNonEmpty(out.DeliveryTo, b.DeliveryTo)
	out.DeliveryStatus = studioFirstNonEmpty(out.DeliveryStatus, b.DeliveryStatus)
	out.DeliveryError = studioFirstNonEmpty(out.DeliveryError, b.DeliveryError)
	out.HasBrowserTrace = out.HasBrowserTrace || b.HasBrowserTrace
	out.BrowserEvents += b.BrowserEvents
	if out.Status == "" || out.Status == "unknown" || (out.Status == "success" && b.Status == "failed") {
		out.Status = b.Status
		out.Ok = b.Ok
	}
	if out.Status == "failed" {
		out.Ok = false
	}
	out.EventCount += b.EventCount
	return out
}

func runLedgerHasBrowserTrace(events []message.Event) bool {
	return runLedgerBrowserEventCount(events) > 0
}

func runLedgerBrowserEventCount(events []message.Event) int {
	count := 0
	for _, ev := range events {
		if runLedgerIsBrowserEvent(ev) {
			count++
		}
	}
	return count
}

func runLedgerIsBrowserEvent(ev message.Event) bool {
	if !strings.HasPrefix(ev.Type, "tool.") {
		return false
	}
	name := strings.ToLower(runLedgerPayloadString(ev.Payload, "name"))
	return strings.Contains(name, "browser") ||
		strings.Contains(name, "playwright") ||
		strings.Contains(name, "puppeteer") ||
		strings.Contains(name, "computer") ||
		strings.Contains(name, "navigate") ||
		strings.Contains(name, "screenshot") ||
		strings.Contains(name, "page_")
}

func runLedgerPayloadString(payload any, key string) string {
	if m, ok := payload.(map[string]any); ok {
		return fmt.Sprint(m[key])
	}
	if m, ok := payload.(map[string]string); ok {
		return m[key]
	}
	return ""
}

func runLedgerContainsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
