package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// buildStructuredReasoningBuiltin implements a safe native counterpart to
// @modelcontextprotocol/server-sequential-thinking. The model supplies only a
// concise decision checkpoint; Soulacy never requests or persists private
// chain-of-thought text.
func buildStructuredReasoningBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "structured_reasoning",
		Description: "Track a complex problem as concise, auditable checkpoints. Use when a task needs revision, branching, or several dependent decisions. Provide conclusions and evidence only, never private chain-of-thought.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"checkpoint":       map[string]any{"type": "string", "description": "A concise conclusion, decision, or open question; do not include hidden chain-of-thought."},
				"step":             map[string]any{"type": "integer", "minimum": 1},
				"total_steps":      map[string]any{"type": "integer", "minimum": 1},
				"needs_more":       map[string]any{"type": "boolean"},
				"revises_step":     map[string]any{"type": "integer", "minimum": 1},
				"branch_from_step": map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"checkpoint", "step", "total_steps", "needs_more"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			checkpoint := strings.TrimSpace(fmt.Sprint(args["checkpoint"]))
			if checkpoint == "" || checkpoint == "<nil>" {
				return "", fmt.Errorf("structured_reasoning: checkpoint is required")
			}
			if len(checkpoint) > 2000 {
				return "", fmt.Errorf("structured_reasoning: checkpoint must be 2000 characters or fewer")
			}
			step, ok := positiveInt(args["step"])
			if !ok {
				return "", fmt.Errorf("structured_reasoning: step must be a positive integer")
			}
			total, ok := positiveInt(args["total_steps"])
			if !ok || total < step {
				return "", fmt.Errorf("structured_reasoning: total_steps must be at least step")
			}
			needsMore, ok := args["needs_more"].(bool)
			if !ok {
				return "", fmt.Errorf("structured_reasoning: needs_more must be boolean")
			}
			out := map[string]any{
				"checkpoint": checkpoint, "step": step, "total_steps": total,
				"needs_more": needsMore, "next_step": step + 1,
				"status": map[bool]string{true: "continue", false: "ready_to_synthesize"}[needsMore],
			}
			for _, key := range []string{"revises_step", "branch_from_step"} {
				if raw, exists := args[key]; exists {
					value, valid := positiveInt(raw)
					if !valid || value > step {
						return "", fmt.Errorf("structured_reasoning: %s must reference the current or an earlier step", key)
					}
					out[key] = value
				}
			}
			encoded, err := json.Marshal(out)
			return string(encoded), err
		},
	}
}

func positiveInt(value any) (int, bool) {
	var n int
	switch v := value.(type) {
	case int:
		n = v
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		n = int(v)
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		n = int(i)
	default:
		return 0, false
	}
	return n, n > 0
}
