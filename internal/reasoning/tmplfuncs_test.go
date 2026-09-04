package reasoning

import "testing"

func TestTmplPluckJoin_ArgOrderTolerant(t *testing.T) {
	articles := []any{
		map[string]any{"url": "a", "title": "A"},
		map[string]any{"url": "b", "title": "B"},
	}
	// Sprig order: pluck "url" .articles
	out1, err := renderTemplate(`{{ pluck "url" .articles | toJson }}`, map[string]any{"articles": articles})
	if err != nil {
		t.Fatalf("sprig-order pluck: %v", err)
	}
	// Reversed order: pluck .articles "url" (the model's slip)
	out2, err := renderTemplate(`{{ pluck .articles "url" | toJson }}`, map[string]any{"articles": articles})
	if err != nil {
		t.Fatalf("reversed-order pluck should be tolerated: %v", err)
	}
	if out1 != `["a","b"]` || out2 != `["a","b"]` {
		t.Errorf("pluck mismatch: %q / %q", out1, out2)
	}
	// join both orders
	j1, err := renderTemplate(`{{ join ", " (pluck "url" .articles) }}`, map[string]any{"articles": articles})
	if err != nil || j1 != "a, b" {
		t.Errorf("join sprig-order: %q err=%v", j1, err)
	}
}
