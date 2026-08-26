package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStructuredReasoningCheckpoint(t *testing.T) {
	tool := buildStructuredReasoningBuiltin()
	out, err := tool.Handler(context.Background(), map[string]any{
		"checkpoint": "Compare both deployment options.", "step": float64(2),
		"total_steps": float64(4), "needs_more": true, "branch_from_step": float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "continue" || got["next_step"] != float64(3) {
		t.Fatalf("unexpected result: %s", out)
	}
}

func TestStructuredReasoningRejectsInvalidReferencesAndLongText(t *testing.T) {
	tool := buildStructuredReasoningBuiltin()
	_, err := tool.Handler(context.Background(), map[string]any{
		"checkpoint": "x", "step": float64(1), "total_steps": float64(1),
		"needs_more": false, "revises_step": float64(2),
	})
	if err == nil || !strings.Contains(err.Error(), "earlier step") {
		t.Fatalf("expected reference validation, got %v", err)
	}
	_, err = tool.Handler(context.Background(), map[string]any{
		"checkpoint": strings.Repeat("x", 2001), "step": float64(1),
		"total_steps": float64(1), "needs_more": false,
	})
	if err == nil {
		t.Fatal("expected long checkpoint rejection")
	}
}
