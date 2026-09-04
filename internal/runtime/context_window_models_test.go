package runtime

import "testing"

// The failure this fixes: a Travel Advisor agent on ollama-cloud/glm-5.2 made
// eight MCP tool calls, and the engine logged
//
//	engine: trimmed history to fit context window
//	{"model":"glm-5.2","dropped_messages":6,"input_budget_tokens":31744}
//
// on every turn. glm matched nothing in the table, so it took the unrecognised
// hosted-model budget of 32768 (minus 1024 reserved for the completion = 31744)
// against a model whose real window is 128k+. By the final synthesis the tool
// results had all been dropped and the model returned 0 output tokens — the user
// got an empty reply with no error.
func TestLargeOpenWeightModelsGetTheirRealWindow(t *testing.T) {
	for _, model := range []string{
		"glm-5.2", "glm-5.1", "deepseek-v4-pro", "kimi-k3", "kimi-k2.6",
		"minimax-m3", "nemotron-3-ultra", "gpt-oss:120b",
	} {
		if got := modelContextLimit("ollama-cloud", model); got < 128000 {
			t.Errorf("modelContextLimit(ollama-cloud, %q) = %d, want the model's real window (>=128000)", model, got)
		}
		// Served locally the operator's num_ctx may genuinely be small, so stay
		// conservative there — but never as low as the unknown-model default,
		// which is what caused the over-trimming.
		if got := modelContextLimit("ollama", model); got <= defaultContextLimit {
			t.Errorf("modelContextLimit(ollama, %q) = %d, want more than the unknown default %d", model, got, defaultContextLimit)
		}
	}
}

// A model released after this table was last edited is, by definition,
// unrecognised. On a hosted provider it must not be assumed tiny: guessing low
// silently deletes history, while guessing high costs at most one rejected call
// that the context-exceeded retry absorbs.
func TestUnknownHostedModelIsNotAssumedTiny(t *testing.T) {
	if got := modelContextLimit("ollama-cloud", "some-model-released-next-year"); got < 128000 {
		t.Errorf("unknown hosted model budget = %d, want >=128000", got)
	}
	// Local stays conservative — a local Ollama really can default to a small
	// num_ctx, and there the retry is the right safety net rather than the guess.
	if got := modelContextLimit("ollama", "some-model-released-next-year"); got != defaultContextLimit {
		t.Errorf("unknown local model budget = %d, want the conservative default %d", got, defaultContextLimit)
	}
}

// Guard the arithmetic the log line reported, so the specific regression is
// pinned rather than only the table entry.
func TestGLMBudgetLeavesRoomForToolResults(t *testing.T) {
	const reserveForCompletion = 1024
	budget := modelContextLimit("ollama-cloud", "glm-5.2") - reserveForCompletion
	if budget == 31744 {
		t.Fatal("still the old 31744-token budget that dropped the run's tool results")
	}
	if budget < 100000 {
		t.Errorf("input budget = %d, too small to hold a multi-step tool run", budget)
	}
}
