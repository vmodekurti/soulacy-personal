package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

// Opt-in evaluation against an already-installed local model. Only a temporary
// synthetic file is readable; no production data, downloads or cloud calls.
func TestModelPreparationLiveLocalModel(t *testing.T) {
	model := os.Getenv("SOULACY_MODEL_PREPARATION_LIVE_MODEL")
	if model == "" {
		t.Skip("set SOULACY_MODEL_PREPARATION_LIVE_MODEL for a real local-model evaluation")
	}
	e, d, _ := modelPreparationEngine(t)
	e.llmRouter.Register(llm.NewOllamaProvider("http://127.0.0.1:11434", model, "5m", nil))
	d.LLM.Provider, d.LLM.Model = "ollama", ""
	d.LLM.MaxTokens = 1024
	d.MaxTurns = 3
	d.Budget = nil
	d.Builtins = strListPtr("read_file")
	d.SystemPrompt = "Read the supplied release fixture using read_file. Report its exact release code and whether ALL its checks passed. Do not invent a pass. Do not perform any writes."
	e.loader.Register(d)
	e.SetTimeoutHierarchy(20*time.Second, 120*time.Second, 120*time.Second, 240*time.Second)
	dir := t.TempDir()
	if err := e.SetFilesystemRoots([]string{dir}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "release.txt")
	if err := os.WriteFile(path, []byte("Release code: TEST-7319\nCheck 1: PASS\nCheck 2: FAIL\nCheck 3: PASS\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prep, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || prep.Profile.Model != model || prep.Profile.Source != "provider_metadata" || prep.Profile.ContextTokens != 16384 {
		t.Fatalf("%+v %v", prep, err)
	}
	t.Logf("model=%s tools=%s reasoning=%s context=%d strategy=%s", prep.Profile.Model, prep.Profile.NativeTools, prep.Profile.Reasoning, prep.Profile.ContextTokens, prep.Strategy)
	read := false
	ctx := WithToolObserver(context.Background(), func(call message.ToolCall, _ string, failed bool) {
		if call.Name == "read_file" && !failed {
			read = true
		}
	})
	reply, err := e.Handle(ctx, testUserMessage(d.ID, "live-preparation", "Read "+path+". Give the release code and say FAIL if any check failed. Never modify files."))
	if err != nil {
		t.Fatal(err)
	}
	result := flattenParts(reply.Parts)
	if !read || !strings.Contains(result, "TEST-7319") || !strings.Contains(strings.ToUpper(result), "FAIL") {
		t.Fatalf("read=%v result=%s", read, result)
	}
	t.Logf("verified reply: %s", result)
}
