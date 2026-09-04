package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestFlowSoftError(t *testing.T) {
	if got := flowSoftError(json.RawMessage(`{"error":"boom"}`)); got != "boom" {
		t.Fatalf("got %q", got)
	}
	if got := flowSoftError(json.RawMessage(`{"error":""}`)); got != "" {
		t.Fatalf("empty error should not count: %q", got)
	}
	if got := flowSoftError(json.RawMessage(`{"ok":true}`)); got != "" {
		t.Fatalf("no error field: %q", got)
	}
	if got := flowSoftError(json.RawMessage(`"just a string"`)); got != "" {
		t.Fatalf("non-object: %q", got)
	}
}

func TestIsAdaptableFailure(t *testing.T) {
	// Shape/format surprises are adaptable.
	for _, r := range []string{
		"Expecting value: line 1 column 1 (char 0)",
		"KeyError: 'results'",
		"can't evaluate field results in type string",
		"tool returned unexpected shape",
	} {
		if !isAdaptableFailure(r) {
			t.Errorf("expected adaptable: %q", r)
		}
	}
	// Real failures are NOT adaptable — salvage can't invent auth or a network.
	for _, r := range []string{
		"401 Unauthorized", "403 forbidden", "429 rate limit exceeded",
		"dial tcp: no such host", "context deadline exceeded",
		"python node refused: consent required",
	} {
		if isAdaptableFailure(r) {
			t.Errorf("expected NOT adaptable: %q", r)
		}
	}
}

func TestBuildAdaptPrompt_CarriesInputAndIntent(t *testing.T) {
	node := sdkr.FlowNode{
		ID:          "parse",
		Kind:        string(sdkr.FlowNodePython),
		Description: "parse the stock JSON into current_price",
		Code:        "import json\ndef run(inputs): return json.loads(inputs['stock_data'])",
	}
	p := buildAdaptPrompt(node, `{"stock_data":"Status: 200 OK\n\n{\"chart\":{}}"}`, "Expecting value: line 1 column 1", json.RawMessage(`{"error":"x"}`))
	for _, want := range []string{"parse the stock JSON", "ACTUAL input", "Status: 200 OK", "Expecting value"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
