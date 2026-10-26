package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestContextReportExplainsSourcesAndCompaction(t *testing.T) {
	def := &agent.Definition{ID: "a"}
	def.LLM.Provider = "test"
	def.LLM.Model = "model"
	before := []llm.ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "system", Content: "## Memory\nremember this"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "answer"},
	}
	after := before[:2]
	report := buildContextReport(def, "tool-followup", before, after, []llm.ToolSchema{{Name: "search"}}, 8192, 4096, 2)
	if !report.Compacted || report.DroppedMessages != 2 {
		t.Fatalf("compaction = %+v", report)
	}
	if !report.Sources["session_memory"] || !report.Sources["tools"] {
		t.Fatalf("sources = %+v", report.Sources)
	}
	if report.EstimatedAfter >= report.EstimatedBefore {
		t.Fatalf("tokens before/after = %d/%d", report.EstimatedBefore, report.EstimatedAfter)
	}
}
