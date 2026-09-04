package studio

import (
	"context"
	"testing"
)

func TestParseSelfTests(t *testing.T) {
	raw := "```json\n{\"tests\":[" +
		"{\"input\":\"hello\",\"assertions\":[{\"target\":\"result\",\"op\":\"exists\",\"value\":\"\"}]}," +
		"{\"input\":\"\",\"assertions\":[{\"target\":\"\",\"op\":\"contains\",\"value\":\"x\"},{\"target\":\"result\",\"op\":\"contains\",\"value\":\"ok\"}]}" +
		"]}\n```"
	got := parseSelfTests(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 tests, got %d: %+v", len(got), got)
	}
	if got[0].Input != "hello" || len(got[0].Assertions) != 1 {
		t.Errorf("test[0] = %+v", got[0])
	}
	// The assertion with an empty target must be dropped.
	if len(got[1].Assertions) != 1 || got[1].Assertions[0].Value != "ok" {
		t.Errorf("test[1] assertions = %+v", got[1].Assertions)
	}
}

func TestSynthesizeTests_BadOutputYieldsNil(t *testing.T) {
	if got := SynthesizeTests(context.Background(), fakeLLM{out: "not json"}, "x", cleanWorkflow(), Catalog{}); got != nil {
		t.Errorf("bad output should yield nil, got %+v", got)
	}
	if got := SynthesizeTests(context.Background(), nil, "x", cleanWorkflow(), Catalog{}); got != nil {
		t.Errorf("nil llm should yield nil, got %+v", got)
	}
}
