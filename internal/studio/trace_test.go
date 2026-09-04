package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildTrace_MirrorAndOrder verifies events are recorded in order with a
// monotonic sequence and that structured data round-trips.
func TestBuildTrace_MirrorAndOrder(t *testing.T) {
	tr := NewMemoryTrace()
	tr.Log("phase", "start", 0, "one")
	tr.Logd("repair", "repair", 1, "two", map[string]any{"problems": []string{"x", "y"}})
	tr.Snapshot("attempt-start", 1, cleanWorkflow())

	evs := tr.Events()
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Errorf("event %d: want seq %d, got %d", i, i+1, e.Seq)
		}
	}
	if evs[1].Data["problems"] == nil {
		t.Errorf("structured data not preserved: %+v", evs[1])
	}
	// Snapshot carries node count + a content hash.
	snap := evs[2].Data
	if snap["nodes"].(int) != 1 {
		t.Errorf("snapshot node count wrong: %+v", snap)
	}
	if snap["hash"] == "" || snap["hash"] == nil {
		t.Errorf("snapshot missing content hash: %+v", snap)
	}
}

// TestBuildTrace_NilSafe asserts every method is a no-op on a nil trace, so the
// loop can be instrumented unconditionally.
func TestBuildTrace_NilSafe(t *testing.T) {
	var tr *BuildTrace // nil
	// None of these may panic.
	tr.Log("k", "p", 0, "m")
	tr.Logd("k", "p", 0, "m", map[string]any{"a": 1})
	tr.Errf("p", 0, context.Canceled, "m")
	tr.Event(BuildEvent{Kind: "attempt", Message: "m"})
	tr.Snapshot("s", 0, cleanWorkflow())
	done := tr.Step("k", "p", 0, "m")
	done(nil, nil)
	if got := tr.Events(); got != nil {
		t.Errorf("nil trace Events() should be nil, got %v", got)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("nil trace Close() should be nil, got %v", err)
	}
	if d := tr.Dump(); d.ID != "" || len(d.Events) != 0 {
		t.Errorf("nil trace Dump() should be empty, got %+v", d)
	}
}

// TestBuildTrace_Step records a completion event with a duration.
func TestBuildTrace_Step(t *testing.T) {
	tr := NewMemoryTrace()
	done := tr.Step("verify", "verify", 2, "running")
	done(nil, map[string]any{"ok": true})
	evs := tr.Events()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Kind != "verify" || evs[0].Attempt != 2 {
		t.Errorf("unexpected step event: %+v", evs[0])
	}
	if evs[0].Data["ok"] != true {
		t.Errorf("step data not recorded: %+v", evs[0])
	}
}

// TestBuildTraceStore_JSONLRoundTrip verifies a build trace is persisted to a
// tail-able JSONL file that round-trips back into TraceEvents.
func TestBuildTraceStore_JSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewBuildTraceStore(10, dir)
	if st.Dir() != dir {
		t.Fatalf("store dir = %q, want %q", st.Dir(), dir)
	}
	tr := st.New("make me a podcast agent")
	tr.Logd("repair", "repair", 1, "fixed a dangling reference", map[string]any{"changed": true})
	tr.Step("verify", "verify", 1, "running")(nil, map[string]any{"ok": true})
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, tr.ID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("trace file not written: %v", err)
	}
	defer f.Close()

	var lines []TraceEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e TraceEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad JSONL line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	// start event + 2 logged events = 3
	if len(lines) != 3 {
		t.Fatalf("want 3 persisted events, got %d", len(lines))
	}
	if lines[0].Kind != "phase" || lines[0].Data["intent"] != "make me a podcast agent" {
		t.Errorf("first persisted event should be the start with intent: %+v", lines[0])
	}
}

// TestBuildTraceStore_Bounding evicts the oldest trace past the cap and keeps
// Get/Latest/List consistent.
func TestBuildTraceStore_Bounding(t *testing.T) {
	st := NewBuildTraceStore(2, "") // memory only
	a := st.New("a")
	b := st.New("b")
	c := st.New("c") // evicts a

	if _, ok := st.Get(a.ID); ok {
		t.Errorf("oldest trace should have been evicted")
	}
	if _, ok := st.Get(b.ID); !ok {
		t.Errorf("trace b should be retained")
	}
	latest, ok := st.Latest()
	if !ok || latest.ID != c.ID {
		t.Errorf("latest should be c, got %+v ok=%v", latest, ok)
	}
	list := st.List()
	if len(list) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(list))
	}
	if list[0].ID != c.ID {
		t.Errorf("list should be newest-first; got %+v", list)
	}
}

// TestBuildUntilWorks_TraceCapturesPhases drives the real loop with a fake LLM
// and scripted verifier and asserts the trace recorded the key phases end to
// end — the regression guard for "every build is debuggable."
func TestBuildUntilWorks_TraceCapturesPhases(t *testing.T) {
	tr := NewMemoryTrace()
	v := &seqVerifier{outs: []VerifyOutcome{
		{OK: false, Real: true, Error: "tool web_search returned nothing"},
		{OK: true, Real: true},
	}}
	fixed := `{"name":"Fixed","trigger":{"type":"manual"},"flow":{"nodes":[` +
		`{"id":"a","kind":"tool","tool":"web_search","input":"{\"query\":\"y\"}","output":"r"}],"entry":"a"}}`
	rep := BuildUntilWorks(context.Background(), fakeLLM{out: fixed}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: v, Trace: tr})
	if !rep.OK || !rep.Verified {
		t.Fatalf("expected build to verify; rep=%+v", rep)
	}

	kinds := map[string]int{}
	var sawResult, sawSnapshot, sawVerifyWithDuration bool
	for _, e := range tr.Events() {
		kinds[e.Kind]++
		switch e.Kind {
		case "result":
			// The canonical final result carries structured verdict data
			// (phase "done"); the loop also mirrors a plain "result" progress line.
			if e.Phase == "done" {
				sawResult = true
				if e.Data["verified"] != true {
					t.Errorf("final result event should report verified=true: %+v", e)
				}
			}
		case "snapshot":
			sawSnapshot = true
		case "verify":
			if e.DurMS >= 0 && e.Phase == "verify" {
				sawVerifyWithDuration = true
			}
		}
	}
	if !sawSnapshot {
		t.Errorf("trace should include a draft snapshot; kinds=%v", kinds)
	}
	if !sawVerifyWithDuration {
		t.Errorf("trace should include a verify step; kinds=%v", kinds)
	}
	if !sawResult {
		t.Errorf("trace should include a final result event; kinds=%v", kinds)
	}
	// The loop's user-facing events are mirrored into the trace too.
	if kinds["attempt"] == 0 {
		t.Errorf("expected attempt events mirrored into the trace; kinds=%v", kinds)
	}
}
