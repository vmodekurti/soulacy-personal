package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/knowledge"
)

func TestKBSearchBuiltinAcceptsAliasesBeforeServiceValidation(t *testing.T) {
	e := newMinimalEngine(t)
	e.knowledge = &knowledge.Service{}
	tool := builtinByName(t, e.buildBuiltins(), "kb_search")

	_, err := tool.Handler(context.Background(), map[string]any{
		"knowledge_base": "AI Docs",
		"q":              "governance",
	})
	if err == nil || strings.Contains(err.Error(), "kb is required") || strings.Contains(err.Error(), "query is required") {
		t.Fatalf("expected aliases to pass field validation and reach service validation, got %v", err)
	}
}

func TestKBWriteBuiltinAcceptsAliasesBeforeServiceValidation(t *testing.T) {
	e := newMinimalEngine(t)
	e.knowledge = &knowledge.Service{}
	tool := builtinByName(t, e.buildBuiltins(), "kb_write")

	_, err := tool.Handler(context.Background(), map[string]any{
		"kb_name":  "AI Docs",
		"document": map[string]any{"title": "One", "tags": []any{"ai", "governance"}},
	})
	if err == nil || strings.Contains(err.Error(), "kb is required") || strings.Contains(err.Error(), "content is required") {
		t.Fatalf("expected aliases to pass field validation and reach service validation, got %v", err)
	}
}
