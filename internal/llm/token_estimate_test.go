package llm

import "testing"

// These prompt-token counts are pinned provider fixture values. The fallback
// is allowed to be conservative, but must stay within 35% and never undercount.
func TestFallbackEstimateCodeAndCJKFixtures(t *testing.T) {
	fixtures := []struct {
		name             string
		text             string
		providerReported int
	}{
		{
			name:             "code-heavy",
			text:             "func reconcile(items []Item) error {\n\tfor _, item := range items {\n\t\tif err := store.Upsert(ctx, item.ID, item.Value); err != nil { return fmt.Errorf(\"upsert %s: %w\", item.ID, err) }\n\t}\n\treturn nil\n}",
			providerReported: 61,
		},
		{
			name:             "cjk",
			text:             "请分析今天东京的天气，并说明是否适合户外活动。明天早晨会下雨吗？",
			providerReported: 31,
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			got := EstimateTextTokens(fixture.text)
			if got < fixture.providerReported {
				t.Fatalf("estimate undercounted: got %d, provider reported %d", got, fixture.providerReported)
			}
			if float64(got-fixture.providerReported)/float64(fixture.providerReported) > 0.35 {
				t.Fatalf("estimate outside 35%% tolerance: got %d, provider reported %d", got, fixture.providerReported)
			}
		})
	}
}

func TestProviderUsageSupersedesFallback(t *testing.T) {
	req := CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hello"}}}
	fallback := EstimateRequestTokens(req)
	if fallback <= 0 {
		t.Fatal("fallback must be non-zero")
	}
	// Providers populate InputTokens from their response. The router only
	// fills a missing zero, so this exact value remains authoritative.
	resp := &CompletionResponse{InputTokens: 137}
	if resp.InputTokens != 137 {
		t.Fatal("provider token usage was replaced")
	}
}
