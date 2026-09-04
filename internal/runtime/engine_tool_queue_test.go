package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentQueueStore_FIFOAndList(t *testing.T) {
	store := newAgentQueueStore()
	if _, err := store.put("docs", json.RawMessage(`{"n":1}`), time.Hour); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if _, err := store.put("docs", json.RawMessage(`{"n":2}`), time.Hour); err != nil {
		t.Fatalf("put second: %v", err)
	}

	items, err := store.list("docs", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("list len = %d, want 2", len(items))
	}

	first, ok, err := store.take("docs")
	if err != nil {
		t.Fatalf("take first: %v", err)
	}
	if !ok || string(first.Item) != `{"n":1}` {
		t.Fatalf("first take = ok %v item %s, want n=1", ok, first.Item)
	}
	second, ok, err := store.take("docs")
	if err != nil {
		t.Fatalf("take second: %v", err)
	}
	if !ok || string(second.Item) != `{"n":2}` {
		t.Fatalf("second take = ok %v item %s, want n=2", ok, second.Item)
	}
	_, ok, err = store.take("docs")
	if err != nil {
		t.Fatalf("take empty: %v", err)
	}
	if ok {
		t.Fatal("empty take returned an item")
	}
}

func TestAgentQueueStore_RejectsUnsafeNamesAndLargeItems(t *testing.T) {
	store := newAgentQueueStore()
	if _, err := store.put("../bad", json.RawMessage(`"x"`), time.Hour); err == nil {
		t.Fatal("put with path-like queue name succeeded")
	}
	large := json.RawMessage(`"` + strings.Repeat("x", agentQueueMaxItemBytes+1) + `"`)
	if _, err := store.put("safe", large, time.Hour); err == nil {
		t.Fatal("put with oversized item succeeded")
	}
}

func TestQueueBuiltins_RoundTripJSON(t *testing.T) {
	e := &Engine{queueStore: newAgentQueueStore()}
	tools := e.buildQueueBuiltins()
	byName := map[string]BuiltinTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	if _, err := byName["queue_put"].Handler(context.Background(), map[string]any{
		"queue": "interactive",
		"item":  map[string]any{"message": "hello", "count": float64(2)},
	}); err != nil {
		t.Fatalf("queue_put: %v", err)
	}

	out, err := byName["queue_take"].Handler(context.Background(), map[string]any{"queue": "interactive"})
	if err != nil {
		t.Fatalf("queue_take: %v", err)
	}
	var got struct {
		OK   bool `json:"ok"`
		Item struct {
			Message string  `json:"message"`
			Count   float64 `json:"count"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output %q: %v", out, err)
	}
	if !got.OK || got.Item.Message != "hello" || got.Item.Count != 2 {
		t.Fatalf("unexpected take result: %+v", got)
	}
}

func TestQueueBuiltins_DefaultQueueAndNames(t *testing.T) {
	e := &Engine{queueStore: newAgentQueueStore()}
	tools := e.buildQueueBuiltins()
	byName := map[string]BuiltinTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	out, err := byName["queue_create"].Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("queue_create default: %v", err)
	}
	if !strings.Contains(out, `"queue":"default"`) {
		t.Fatalf("queue_create output = %s, want default queue", out)
	}
	if _, err := byName["queue_put"].Handler(context.Background(), map[string]any{
		"item": "saved without explicit queue",
	}); err != nil {
		t.Fatalf("queue_put default: %v", err)
	}

	names, err := byName["queue_names"].Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("queue_names: %v", err)
	}
	if !strings.Contains(names, `"queue":"default"`) || !strings.Contains(names, `"count":1`) {
		t.Fatalf("queue_names output = %s, want default count 1", names)
	}

	taken, err := byName["queue_take"].Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("queue_take default: %v", err)
	}
	if !strings.Contains(taken, "saved without explicit queue") {
		t.Fatalf("queue_take output = %s, want default item", taken)
	}
}
