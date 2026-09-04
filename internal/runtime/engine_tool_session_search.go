package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/message"
)

// buildSessionSearchBuiltin lets ReAct agents retrieve relevant prior runs
// without reading action-log files or direct database access.
func (e *Engine) buildSessionSearchBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "session_search",
		Gate:        "",
		Description: "Search this agent's recent completed sessions for matching user requests, replies, channels, and tools. Use during planning when prior work may contain useful context, preferences, or successful tool recipes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language terms to match against recent run inputs, outputs, tools, and channels.",
				},
				"agent_id": map[string]any{
					"type":        "string",
					"description": "Optional agent id to search. Defaults to the current agent.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum matching sessions to return (default 5, max 20).",
				},
			},
			"required": []string{"query"},
		},
		Handler: e.sessionSearch,
	}
}

func (e *Engine) sessionSearch(ctx context.Context, args map[string]any) (string, error) {
	if e.actionLog == nil {
		return "", fmt.Errorf("session_search: action log is unavailable")
	}
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("session_search: query is required")
	}
	agentID := strings.TrimSpace(argString(args, "agent_id"))
	if agentID == "" {
		if inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
			agentID = strings.TrimSpace(inbound.AgentID)
		}
	}
	if agentID == "" {
		return "", fmt.Errorf("session_search: agent_id is required outside an agent run")
	}
	limit := argInt(args, "limit", 5)
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	events, err := e.actionLog.Tail(agentID, 5000)
	if err != nil {
		return "", fmt.Errorf("session_search: %w", err)
	}
	runs := learning.RunsFromRecentEvents(events, agentID, 80)
	terms := strings.Fields(strings.ToLower(query))
	type result struct {
		SessionID string   `json:"session_id"`
		Channel   string   `json:"channel,omitempty"`
		Score     int      `json:"score"`
		UserText  string   `json:"user_text,omitempty"`
		ReplyText string   `json:"reply_text,omitempty"`
		Tools     []string `json:"tools,omitempty"`
	}
	results := make([]result, 0, limit)
	for _, run := range runs {
		if !run.FoundIn && !run.FoundOut {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			run.UserText,
			run.ReplyText,
			run.Channel,
			strings.Join(run.Tools, " "),
		}, " "))
		score := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		results = append(results, result{
			SessionID: run.SessionID,
			Channel:   run.Channel,
			Score:     score,
			UserText:  truncateSessionSearch(run.UserText, 500),
			ReplyText: truncateSessionSearch(run.ReplyText, 800),
			Tools:     run.Tools,
		})
		if len(results) >= limit {
			break
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"agent_id": agentID,
		"query":    query,
		"count":    len(results),
		"results":  results,
	})
	return string(payload), nil
}

func truncateSessionSearch(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}
