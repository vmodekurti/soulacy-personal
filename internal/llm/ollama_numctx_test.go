package llm

import (
	"context"
	"net/http"
	"testing"
)

// The window must fit the model: big enough for Genie on a large model, and
// never larger than a small model can actually serve. The choice is
// deterministic (model-only) so cached profiles do not vary by machine.
func TestChooseNumCtx(t *testing.T) {
	cases := []struct {
		name     string
		modelMax int
		want     int
	}{
		{"unknown model", 0, ollamaNumCtxFloor},                    // no metadata: conservative floor
		{"huge model capped", 262144, ollamaNumCtxTarget},          // capped at the memory-reasonable target
		{"exactly target", ollamaNumCtxTarget, ollamaNumCtxTarget}, // boundary
		{"model below target", 20000, 20000},                       // its own max, below the cap
		{"small model", 8192, 8192},                                // never asked for more than it can serve
		{"tiny model", 4096, 4096},
		{"invalid max", -100, ollamaNumCtxFloor}, // garbage metadata: floor
	}
	for _, c := range cases {
		if got := chooseNumCtx(c.modelMax); got != c.want {
			t.Errorf("%s: chooseNumCtx(%d) = %d, want %d", c.name, c.modelMax, got, c.want)
		}
	}
}

// An operator who pins num_ctx is obeyed — the smart path never overrides an
// explicit choice.
func TestResolveNumCtxRespectsOperatorSetting(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "qwen3-coder:30b", "", map[string]any{"num_ctx": 40960})
	if got := p.resolveNumCtx("qwen3-coder:30b"); got != 40960 {
		t.Fatalf("operator num_ctx should win; got %d", got)
	}
}

// Before a model has been profiled, the request path uses the floor rather than
// nothing (which would let Ollama truncate to its own tiny default).
func TestResolveNumCtxFloorsBeforeProfiling(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	if got := p.resolveNumCtx("llama3"); got != ollamaNumCtxFloor {
		t.Fatalf("unprofiled model should floor at %d; got %d", ollamaNumCtxFloor, got)
	}
}

// ProfileModel is the one place that reads the model's context_length; it must
// cache the chosen window so the request path serves exactly what preflight
// budgeted. This drives ProfileModel against a stub /api/show and then checks
// both the reported ContextTokens and the cached request-path value agree.
func TestProfileModelResolvesAndCachesNumCtx(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "qwen3-coder:30b", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return textResponse(200, `{"capabilities":["completion","tools"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":262144}}`), nil
	})

	profile, err := p.ProfileModel(context.Background(), "qwen3-coder:30b")
	if err != nil {
		t.Fatalf("ProfileModel: %v", err)
	}
	if profile.ContextTokens != ollamaNumCtxTarget {
		t.Errorf("profile ContextTokens = %d, want %d (262144 model capped to target)", profile.ContextTokens, ollamaNumCtxTarget)
	}
	if profile.ContextSource != "resolved" {
		t.Errorf("profile ContextSource = %q, want \"resolved\"", profile.ContextSource)
	}
	if got := p.resolveNumCtx("qwen3-coder:30b"); got != ollamaNumCtxTarget {
		t.Errorf("request path should serve the cached window %d; got %d", ollamaNumCtxTarget, got)
	}
}
