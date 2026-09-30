package runtime

import (
	"encoding/json"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/missions"
	"github.com/soulacy/soulacy/pkg/message"
)

func shouldForceGenieActionPlan(agentID, goal string, tools []llm.ToolSchema) bool {
	return agentID == GenieAgentID && missions.LooksLikeActionGoal(goal) && toolSchemaExists(tools, "plan_action")
}

func genieActionPlanCall(goal string) message.ToolCall {
	return message.ToolCall{
		ID:   "genie-action-plan-" + uuidShort(),
		Name: "plan_action",
		Arguments: map[string]any{
			"goal": goal,
		},
	}
}

func geniePlannedQuestionReply(results []message.ToolResult) string {
	for _, result := range results {
		if normalizeToolCallName(result.Name) != "plan_action" || result.IsError {
			continue
		}
		var payload struct {
			SuggestedReply string `json:"suggested_reply"`
		}
		if err := json.Unmarshal([]byte(result.Content), &payload); err == nil {
			if reply := strings.TrimSpace(payload.SuggestedReply); reply != "" {
				return reply
			}
		}
	}
	return ""
}

// initialGenieActionReply keeps the first clarification framework-shaped so
// local models cannot turn a valid fallback plan into a refusal or ask every
// future requirement at once. Later turns resume the ordinary model loop.
func initialGenieActionReply(turn int, forced bool, results []message.ToolResult) string {
	if turn != 0 || !forced {
		return ""
	}
	return geniePlannedQuestionReply(results)
}
