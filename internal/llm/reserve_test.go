package llm

import (
	"context"
	"strings"
	"testing"
)

// The output reserve must be sized to the model that serves it. The invariant
// that matters for shared-context models: the reserve never exceeds half the
// window, so the prompt always keeps at least half.
func TestReconcileOutputReserve(t *testing.T) {
	cases := []struct {
		name                string
		ctx, out, req, want int
	}{
		{"shared window caps reserve at half", 32768, 0, 31200, 16384}, // the production bug
		{"reserve under half is kept", 32768, 0, 8192, 8192},
		{"reserve equal to window halves", 32768, 0, 32768, 16384},
		{"unset reserve gets a safe default", 32768, 0, 0, 1024},
		{"tiny window still halves", 4096, 0, 9000, 2048},
		{"cloud output ceiling caps", 0, 2048, 8192, 2048}, // separate ceilings, no shared window
		{"no ceilings leaves the request alone", 0, 0, 5000, 5000},
	}
	for _, c := range cases {
		p := ModelProfile{ContextTokens: c.ctx, OutputTokens: c.out}
		if got := ReconcileOutputReserve(p, c.req); got != c.want {
			t.Errorf("%s: ReconcileOutputReserve(ctx=%d,out=%d,req=%d) = %d, want %d",
				c.name, c.ctx, c.out, c.req, got, c.want)
		}
	}
}

// Regression for the exact production failure: a 32768-token window with a
// 31200-token reserve left only 1568 tokens for input, so every real Genie
// prompt failed preflight with "prompt exceeds the evidenced input budget".
// The reserve must now be capped to half the window so the prompt fits, and the
// request must actually be served (not rejected).
func TestApplyProfileLimitsReserveCannotStarveInput(t *testing.T) {
	profile := profileFixture("model")
	profile.ContextTokens = 32768
	req := CompletionRequest{
		// ~5k tokens: far above the broken 1568 budget, well under 16384.
		Messages:  []ChatMessage{{Role: "user", Content: strings.Repeat("word ", 3000)}},
		MaxTokens: 31200,
	}
	var served CompletionRequest
	fake := &profiledFake{
		fakeProvider: fakeProvider{id: "test"},
		model:        "model",
		profile:      func(context.Context, string) (ModelProfile, error) { return profile, nil },
		complete:     func(r CompletionRequest) { served = r },
	}
	r := NewRouter("test")
	r.Register(fake)
	if _, err := r.Complete(context.Background(), "", req); err != nil {
		t.Fatalf("reserve should have been capped to fit the prompt; got preflight error: %v", err)
	}
	if served.MaxTokens != 16384 {
		t.Fatalf("served reserve = %d, want 16384 (half the 32768 window)", served.MaxTokens)
	}
}
