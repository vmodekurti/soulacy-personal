package llm

import (
	"testing"
	"time"
)

// The OpenAI-compatible provider must default to the generous long-generation
// timeout (not the old 120s), because the Studio builder issues large
// non-streaming completions to cloud reasoning models that legitimately run for
// minutes. A 120s bound reproduced "timeout awaiting response headers".
func TestOpenAIProvider_DefaultTimeout(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "k", "gpt")
	if p.client == nil {
		t.Fatal("client not initialised")
	}
	if p.client.Timeout != DefaultOpenAITimeout {
		t.Errorf("default timeout = %v, want %v", p.client.Timeout, DefaultOpenAITimeout)
	}
	if DefaultOpenAITimeout <= 120*time.Second {
		t.Errorf("default timeout %v must exceed the old 120s cap", DefaultOpenAITimeout)
	}
}

// SetRequestTimeout honours a positive override and ignores non-positive values.
func TestOpenAIProvider_SetRequestTimeout(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "k", "gpt")

	p.SetRequestTimeout(10 * time.Minute)
	if p.client.Timeout != 10*time.Minute {
		t.Errorf("override timeout = %v, want 10m", p.client.Timeout)
	}

	// Zero / negative must not clobber the current client.
	p.SetRequestTimeout(0)
	if p.client.Timeout != 10*time.Minute {
		t.Errorf("zero override changed timeout to %v, want it unchanged", p.client.Timeout)
	}
	p.SetRequestTimeout(-5 * time.Second)
	if p.client.Timeout != 10*time.Minute {
		t.Errorf("negative override changed timeout to %v, want it unchanged", p.client.Timeout)
	}
}

// providerRequestTimeout parses a duration string and falls back to 0 (keep the
// provider default) for empty or invalid input.
func TestProviderRequestTimeout(t *testing.T) {
	cases := []struct {
		in   any
		want time.Duration
	}{
		{"300s", 300 * time.Second},
		{"10m", 10 * time.Minute},
		{"  45s ", 45 * time.Second},
		{"", 0},
		{"nonsense", 0},
		{"-1s", 0},
		{"0s", 0},
	}
	for _, c := range cases {
		got := providerRequestTimeout(map[string]any{"request_timeout": c.in})
		if got != c.want {
			t.Errorf("request_timeout=%q → %v, want %v", c.in, got, c.want)
		}
	}
	// Absent key → 0 (default kept).
	if got := providerRequestTimeout(map[string]any{}); got != 0 {
		t.Errorf("absent key → %v, want 0", got)
	}
}
