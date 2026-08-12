// flow_parallel_test.go — Story ST-06: kind=parallel fan-out and join policies.
//
// The regression that motivated all of this: a graph that forked on the canvas
// executed exactly ONE fork, because the walker took "the first truthy edge".
// These tests pin the new contract — every eligible edge runs, concurrently,
// with a declared join policy — and they prove the concurrency with barriers
// rather than sleeps, so they neither flake nor pass on a sequential engine.
package reasoning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// parRunner is a concurrency-safe FlowRunNode: branches call it from several
// goroutines at once, so unlike recRunner it guards its bookkeeping.
type parRunner struct {
	mu      sync.Mutex
	calls   []string
	results map[string]string
	errs    map[string]error
	// hook optionally takes over a node's execution (to block on a barrier, wait
	// for cancellation, …). handled=false falls through to results/errs.
	hook func(ctx context.Context, node sdkr.FlowNode) (out json.RawMessage, err error, handled bool)
}

func (r *parRunner) run(ctx context.Context, node sdkr.FlowNode, _ string) (json.RawMessage, error) {
	r.mu.Lock()
	r.calls = append(r.calls, node.ID)
	r.mu.Unlock()
	if r.hook != nil {
		if out, err, handled := r.hook(ctx, node); handled {
			return out, err
		}
	}
	if err := r.errs[node.ID]; err != nil {
		return nil, err
	}
	if out, ok := r.results[node.ID]; ok {
		return json.RawMessage(out), nil
	}
	return json.RawMessage(fmt.Sprintf(`{"node":%q}`, node.ID)), nil
}

func (r *parRunner) executed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *parRunner) count(id string) int {
	n := 0
	for _, c := range r.executed() {
		if c == id {
			n++
		}
	}
	return n
}

// recorder is a concurrency-safe Observe hook.
type recorder struct {
	mu   sync.Mutex
	recs []FlowNodeRun
}

func (rc *recorder) hooks() FlowHooks {
	return FlowHooks{Observe: func(rec FlowNodeRun) {
		rc.mu.Lock()
		rc.recs = append(rc.recs, rec)
		rc.mu.Unlock()
	}}
}

func (rc *recorder) all() []FlowNodeRun {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]FlowNodeRun(nil), rc.recs...)
}

func (rc *recorder) byNode(id string) []FlowNodeRun {
	var out []FlowNodeRun
	for _, rec := range rc.all() {
		if rec.NodeID == id {
			out = append(out, rec)
		}
	}
	return out
}

// fanOutSpec builds fan(parallel) → one tool node per branch id, each wired to
// the barrier when the fan declares one.
func fanOutSpec(fan sdkr.FlowNode, branches ...string) sdkr.FlowSpec {
	fan.ID = "fan"
	fan.Kind = sdkr.FlowNodeParallel
	nodes := []sdkr.FlowNode{fan}
	var edges []sdkr.FlowEdge
	for _, b := range branches {
		nodes = append(nodes, sdkr.FlowNode{ID: b, Tool: "t", Output: b})
		edges = append(edges, sdkr.FlowEdge{From: "fan", To: b})
		if fan.JoinNode != "" {
			edges = append(edges, sdkr.FlowEdge{From: b, To: fan.JoinNode})
		}
	}
	if fan.JoinNode != "" {
		nodes = append(nodes, sdkr.FlowNode{ID: fan.JoinNode, Tool: "t", Output: "merged"})
	}
	return sdkr.FlowSpec{Nodes: nodes, Edges: edges, Entry: "fan"}
}

func mustCompile(t *testing.T, spec sdkr.FlowSpec) *FlowGraph {
	t.Helper()
	g, err := CompileFlow(spec)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return g
}

// ── compile-time contract ────────────────────────────────────────────────────

func TestCompileFlow_ParallelValidation(t *testing.T) {
	cases := map[string]struct {
		spec sdkr.FlowSpec
		want string
	}{
		"single outgoing edge is not a fan-out": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{{ID: "fan", Kind: sdkr.FlowNodeParallel}, {ID: "a", Tool: "t"}},
				Edges: []sdkr.FlowEdge{{From: "fan", To: "a"}},
			},
			want: "at least 2",
		},
		"no outgoing edges": {
			spec: sdkr.FlowSpec{Nodes: []sdkr.FlowNode{{ID: "fan", Kind: sdkr.FlowNodeParallel}}},
			want: "at least 2",
		},
		"join node does not exist": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{
					{ID: "fan", Kind: sdkr.FlowNodeParallel, JoinNode: "ghost"},
					{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"},
				},
				Edges: []sdkr.FlowEdge{{From: "fan", To: "a"}, {From: "fan", To: "b"}},
			},
			want: "does not exist",
		},
		"join node unreachable from a branch": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{
					{ID: "fan", Kind: sdkr.FlowNodeParallel, JoinNode: "join"},
					{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"}, {ID: "join", Tool: "t"},
				},
				Edges: []sdkr.FlowEdge{
					{From: "fan", To: "a"}, {From: "fan", To: "b"},
					{From: "a", To: "join"}, // b never converges
				},
			},
			want: `branch starting at "b" cannot reach it`,
		},
		"branch terminates before the barrier": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{
					{ID: "fan", Kind: sdkr.FlowNodeParallel, JoinNode: "join"},
					{ID: "a", Tool: "t"}, {ID: "join", Tool: "t"},
				},
				Edges: []sdkr.FlowEdge{
					{From: "fan", To: "a"}, {From: "fan", To: "end"}, {From: "a", To: "join"},
				},
			},
			want: "terminates before the barrier",
		},
		"quorum below one": {
			spec: fanOutSpec(sdkr.FlowNode{Join: sdkr.JoinQuorum}, "a", "b"),
			want: "must be between 1",
		},
		"quorum above branch count": {
			spec: fanOutSpec(sdkr.FlowNode{Join: sdkr.JoinQuorum, JoinQuorum: 3}, "a", "b"),
			want: "must be between 1",
		},
		"quorum size without quorum policy": {
			spec: fanOutSpec(sdkr.FlowNode{Join: sdkr.JoinAll, JoinQuorum: 2}, "a", "b"),
			want: "only applies to join: quorum",
		},
		"unknown join policy": {
			spec: fanOutSpec(sdkr.FlowNode{Join: "most"}, "a", "b"),
			want: "unknown join policy",
		},
		"parallel cannot also for_each": {
			spec: fanOutSpec(sdkr.FlowNode{ForEach: `["x"]`}, "a", "b"),
			want: "cannot combine kind=parallel with for_each",
		},
		"join settings on a non-parallel node": {
			spec: sdkr.FlowSpec{Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", Join: sdkr.JoinAny}}},
			want: "not kind=parallel",
		},
		"node names itself as its barrier": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{
					{ID: "fan", Kind: sdkr.FlowNodeParallel, JoinNode: "fan"},
					{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"},
				},
				Edges: []sdkr.FlowEdge{
					{From: "fan", To: "a"}, {From: "fan", To: "b"},
					{From: "a", To: "fan"}, {From: "b", To: "fan"},
				},
			},
			want: "names itself",
		},
		"nested parallels sharing one barrier": {
			spec: sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{
					{ID: "outer", Kind: sdkr.FlowNodeParallel, JoinNode: "join"},
					{ID: "inner", Kind: sdkr.FlowNodeParallel, JoinNode: "join"},
					{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"}, {ID: "c", Tool: "t"},
					{ID: "join", Tool: "t"},
				},
				Edges: []sdkr.FlowEdge{
					{From: "outer", To: "inner"}, {From: "outer", To: "c"},
					{From: "inner", To: "a"}, {From: "inner", To: "b"},
					{From: "a", To: "join"}, {From: "b", To: "join"}, {From: "c", To: "join"},
				},
			},
			want: "sharing join_node",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := CompileFlow(tc.spec)
			if err == nil {
				t.Fatalf("expected a compile error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A well-formed fan-out compiles, and the parallel node stays structural.
	g := mustCompile(t, fanOutSpec(sdkr.FlowNode{Join: sdkr.JoinQuorum, JoinQuorum: 2, JoinNode: "join"}, "a", "b", "c"))
	if !sdkr.IsStructuralKind(g.Node("fan").Kind) {
		t.Fatalf("kind=parallel must be structural (it performs no work itself)")
	}
}

// ── the regression: every branch runs ────────────────────────────────────────

func TestRunFlow_ParallelRunsEveryBranch(t *testing.T) {
	g := mustCompile(t, fanOutSpec(sdkr.FlowNode{Output: "fanned"}, "left", "right"))
	r := &parRunner{results: map[string]string{
		"left":  `{"side":"left"}`,
		"right": `{"side":"right"}`,
	}}

	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if r.count("left") != 1 || r.count("right") != 1 {
		t.Fatalf("both branches must execute exactly once, got %v", r.executed())
	}
	if want := `[{"side":"left"},{"side":"right"}]`; string(out) != want {
		t.Fatalf("aggregate=%s, want %s", out, want)
	}
}

// ── the concurrency is real, not simulated ───────────────────────────────────

func TestRunFlow_ParallelBranchesOverlapInTime(t *testing.T) {
	g := mustCompile(t, fanOutSpec(sdkr.FlowNode{}, "a", "b", "c"))

	entered := make(chan string, 3)
	release := make(chan struct{})
	r := &parRunner{hook: func(_ context.Context, node sdkr.FlowNode) (json.RawMessage, error, bool) {
		entered <- node.ID
		// A sequential walker deadlocks here: it cannot reach the second branch
		// until the first returns, so the barrier below times out instead of
		// passing by luck.
		<-release
		return json.RawMessage(`{"ok":true}`), nil, true
	}}
	rc := &recorder{}

	done := make(chan error, 1)
	go func() {
		_, err := RunFlow(context.Background(), g, map[string]any{}, r.run, rc.hooks())
		done <- err
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of 3 branches were in flight — the fan-out is still sequential", i)
		}
	}
	// Nothing has been released yet, so at THIS instant all three branches are
	// simultaneously inside the runner. Every branch record must therefore report
	// a StartedAt earlier than this moment — that is the overlap, computed from
	// the trace rather than assumed from wall-clock luck.
	releasedAt := time.Now().UTC()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	for _, id := range []string{"a", "b", "c"} {
		recs := rc.byNode(id)
		if len(recs) != 1 {
			t.Fatalf("node %q: %d records, want 1", id, len(recs))
		}
		if recs[0].StartedAt.IsZero() {
			t.Fatalf("node %q: record carries no StartedAt, overlap is not computable", id)
		}
		// Equal is valid because trace timestamps are normalised to a coarser
		// precision than time.Now on some platforms.
		if recs[0].StartedAt.After(releasedAt) {
			t.Fatalf("node %q started at %v, after the moment all three were in flight (%v)", id, recs[0].StartedAt, releasedAt)
		}
		if recs[0].BranchID == "" || recs[0].ParallelGroup != "fan" {
			t.Fatalf("node %q: record must name its branch and group, got %+v", id, recs[0])
		}
	}
	keys := map[string]bool{}
	for _, rec := range rc.all() {
		if rec.VisitKey == "" || keys[rec.VisitKey] {
			t.Fatalf("visit keys must be distinct per record, saw %q twice", rec.VisitKey)
		}
		keys[rec.VisitKey] = true
	}
}

// ── join policies ────────────────────────────────────────────────────────────

func TestRunFlow_JoinPolicies(t *testing.T) {
	boom := errors.New("branch exploded")

	// hangs blocks until the group cancels it — the only honest way to test that
	// any/quorum really stop waiting for the losers.
	hangs := func(ids ...string) func(context.Context, sdkr.FlowNode) (json.RawMessage, error, bool) {
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		return func(ctx context.Context, node sdkr.FlowNode) (json.RawMessage, error, bool) {
			if !set[node.ID] {
				return nil, nil, false
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err(), true
			case <-time.After(5 * time.Second):
				return nil, errors.New("branch was never cancelled"), true
			}
		}
	}

	cases := []struct {
		name      string
		fan       sdkr.FlowNode
		branches  []string
		errs      map[string]error
		hook      func(context.Context, sdkr.FlowNode) (json.RawMessage, error, bool)
		wantErr   string
		wantAgg   string
		wantRuns  map[string]int
		wantNoRun []string
	}{
		{
			name:     "all succeeds when every branch does",
			fan:      sdkr.FlowNode{Join: sdkr.JoinAll},
			branches: []string{"a", "b"},
			wantAgg:  `[{"node":"a"},{"node":"b"}]`,
		},
		{
			name:     "all fails naming the failing branch",
			fan:      sdkr.FlowNode{Join: sdkr.JoinAll},
			branches: []string{"a", "b"},
			errs:     map[string]error{"b": boom},
			wantErr:  `branch "b"`,
		},
		{
			name:     "empty join defaults to all",
			fan:      sdkr.FlowNode{},
			branches: []string{"a", "b"},
			errs:     map[string]error{"a": boom},
			wantErr:  "branch exploded",
		},
		{
			name:     "any takes the first success and cancels the rest",
			fan:      sdkr.FlowNode{Join: sdkr.JoinAny},
			branches: []string{"slow", "quick"},
			hook:     hangs("slow"),
			wantAgg:  `[null,{"node":"quick"}]`,
		},
		{
			name:     "any fails only when every branch fails",
			fan:      sdkr.FlowNode{Join: sdkr.JoinAny},
			branches: []string{"a", "b"},
			errs:     map[string]error{"a": boom, "b": boom},
			wantErr:  "all 2 branches failed",
		},
		{
			name:     "quorum stops at k successes",
			fan:      sdkr.FlowNode{Join: sdkr.JoinQuorum, JoinQuorum: 2},
			branches: []string{"a", "b", "slow"},
			hook:     hangs("slow"),
			wantAgg:  `[{"node":"a"},{"node":"b"},null]`,
		},
		{
			name:     "quorum fails once it becomes unreachable",
			fan:      sdkr.FlowNode{Join: sdkr.JoinQuorum, JoinQuorum: 3},
			branches: []string{"a", "b", "c"},
			errs:     map[string]error{"c": boom},
			wantErr:  `fewer than 3 of 3 branches succeeded; branch "c": `,
		},
		{
			name:     "best_effort never fails and nulls the failures",
			fan:      sdkr.FlowNode{Join: sdkr.JoinBestEffort},
			branches: []string{"a", "b", "c"},
			errs:     map[string]error{"b": boom},
			wantAgg:  `[{"node":"a"},null,{"node":"c"}]`,
		},
		{
			name:     "predicates still gate which branches fan out",
			fan:      sdkr.FlowNode{Join: sdkr.JoinAll},
			branches: []string{"a", "b"},
			wantAgg:  `[{"node":"a"}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := fanOutSpec(tc.fan, tc.branches...)
			if tc.name == "predicates still gate which branches fan out" {
				spec.Edges[1].If = "false"
			}
			g := mustCompile(t, spec)
			r := &parRunner{errs: tc.errs, hook: tc.hook}

			out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
			switch {
			case tc.wantErr != "":
				if err == nil {
					t.Fatalf("expected an error mentioning %q, got result %s", tc.wantErr, out)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("RunFlow: %v", err)
				}
				if string(out) != tc.wantAgg {
					t.Fatalf("aggregate=%s, want %s", out, tc.wantAgg)
				}
			}
			if tc.name == "predicates still gate which branches fan out" && r.count("b") != 0 {
				t.Fatalf("a predicate-gated branch must not execute: %v", r.executed())
			}
		})
	}
}

// ── determinism ──────────────────────────────────────────────────────────────

func TestRunFlow_ParallelAggregateOrderIsDeclarationOrder(t *testing.T) {
	branches := []string{"b1", "b2", "b3", "b4"}
	want := `[{"node":"b1"},{"node":"b2"},{"node":"b3"},{"node":"b4"}]`

	// Each repetition releases the branches in a different order, so completion
	// order is genuinely shuffled between runs while the aggregate must not move.
	orders := [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {2, 0, 3, 1}, {1, 3, 0, 2}}
	for i, order := range orders {
		g := mustCompile(t, fanOutSpec(sdkr.FlowNode{}, branches...))
		gates := map[string]chan struct{}{}
		for _, b := range branches {
			gates[b] = make(chan struct{})
		}
		entered := make(chan string, len(branches))
		r := &parRunner{hook: func(_ context.Context, node sdkr.FlowNode) (json.RawMessage, error, bool) {
			entered <- node.ID
			<-gates[node.ID]
			return nil, nil, false
		}}

		type res struct {
			out json.RawMessage
			err error
		}
		done := make(chan res, 1)
		go func() {
			out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
			done <- res{out, err}
		}()
		for range branches {
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatalf("run %d: branches never all started", i)
			}
		}
		for _, idx := range order {
			close(gates[branches[idx]])
		}
		got := <-done
		if got.err != nil {
			t.Fatalf("run %d: RunFlow: %v", i, got.err)
		}
		if string(got.out) != want {
			t.Fatalf("run %d (completion order %v): aggregate=%s, want %s", i, order, got.out, want)
		}
	}
}

func TestRunFlow_ParallelVarsMergeInDeclarationOrder(t *testing.T) {
	// one and two both write "shared"; three writes its own var. Declaration
	// order decides the winner regardless of who finishes first, and a branch
	// that only READ a variable must not clobber a sibling's write to it.
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "seed", Tool: "t", Output: "seed"},
			{ID: "fan", Kind: sdkr.FlowNodeParallel, JoinNode: "join"},
			{ID: "one", Tool: "t", Output: "shared"},
			{ID: "two", Tool: "t", Output: "shared"},
			{ID: "three", Tool: "t", Output: "only3", Input: `{"read":"{{ .seed }}"}`},
			{ID: "join", Tool: "t", Input: `{"shared":"{{ .shared }}","only3":"{{ .only3 }}","seed":"{{ .seed }}"}`},
		},
		Edges: []sdkr.FlowEdge{
			{From: "seed", To: "fan"},
			{From: "fan", To: "one"}, {From: "fan", To: "two"}, {From: "fan", To: "three"},
			{From: "one", To: "join"}, {From: "two", To: "join"}, {From: "three", To: "join"},
		},
		Entry: "seed",
	}

	for i := 0; i < 8; i++ {
		g := mustCompile(t, spec)
		gates := map[string]chan struct{}{"one": make(chan struct{}), "two": make(chan struct{})}
		entered := make(chan string, 2)
		r := &parRunner{
			results: map[string]string{
				"seed": `"s"`, "one": `"from-one"`, "two": `"from-two"`, "three": `"from-three"`,
			},
			hook: func(_ context.Context, node sdkr.FlowNode) (json.RawMessage, error, bool) {
				if gate, ok := gates[node.ID]; ok {
					entered <- node.ID
					<-gate
				}
				return nil, nil, false
			},
		}
		rc := &recorder{}
		done := make(chan error, 1)
		go func() {
			_, err := RunFlow(context.Background(), g, map[string]any{}, r.run, rc.hooks())
			done <- err
		}()
		for range gates {
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("branches never all started")
			}
		}
		// Alternate which branch finishes first between repetitions.
		if i%2 == 0 {
			close(gates["two"])
			close(gates["one"])
		} else {
			close(gates["one"])
			close(gates["two"])
		}
		if err := <-done; err != nil {
			t.Fatalf("RunFlow: %v", err)
		}
		recs := rc.byNode("join")
		if len(recs) != 1 {
			t.Fatalf("barrier ran %d times, want 1", len(recs))
		}
		want := `{"shared":"from-two","only3":"from-three","seed":"s"}`
		if recs[0].Input != want {
			t.Fatalf("run %d: barrier input=%s, want %s", i, recs[0].Input, want)
		}
	}
}

// ── barrier ──────────────────────────────────────────────────────────────────

func TestRunFlow_ParallelBarrierRunsOnceAfterAllBranches(t *testing.T) {
	g := mustCompile(t, fanOutSpec(sdkr.FlowNode{JoinNode: "join", Output: "fanned"}, "a", "b", "c"))
	r := &parRunner{}
	rc := &recorder{}

	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, rc.hooks())
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if r.count("join") != 1 {
		t.Fatalf("barrier must run exactly once, executions: %v", r.executed())
	}
	calls := r.executed()
	if calls[len(calls)-1] != "join" {
		t.Fatalf("barrier must run last, executions: %v", calls)
	}
	if want := `{"node":"join"}`; string(out) != want {
		t.Fatalf("flow result=%s, want the barrier's result %s", out, want)
	}

	// The group itself is observable: one record spanning the whole fan-out,
	// carrying the aggregate, and NOT attributed to any single branch.
	group := rc.byNode("fan")
	if len(group) != 1 {
		t.Fatalf("expected exactly one group record, got %d", len(group))
	}
	if group[0].ParallelGroup != "fan" || group[0].BranchID != "" {
		t.Fatalf("group record must name itself as the group and belong to no branch: %+v", group[0])
	}
	if want := `[{"node":"a"},{"node":"b"},{"node":"c"}]`; string(group[0].Output) != want {
		t.Fatalf("group aggregate=%s, want %s", group[0].Output, want)
	}
	for _, id := range []string{"a", "b", "c"} {
		rec := rc.byNode(id)[0]
		if rec.ParallelGroup != "fan" || !strings.HasPrefix(rec.BranchID, "fan#1[") {
			t.Fatalf("branch record for %q not attributed: %+v", id, rec)
		}
		// The group's span must contain each branch's span.
		if rec.StartedAt.Before(group[0].StartedAt) {
			t.Fatalf("branch %q started before its group: %v < %v", id, rec.StartedAt, group[0].StartedAt)
		}
	}
	if rc.byNode("join")[0].BranchID != "" {
		t.Fatalf("the barrier runs on the main walk, not inside a branch")
	}
}

// ── budgets ──────────────────────────────────────────────────────────────────

func TestRunFlow_ParallelBudgetIsGlobalAcrossBranches(t *testing.T) {
	// Three branches of two nodes each = 6 executions against a budget of 3.
	// A per-branch budget would let this pass; the budget must be shared.
	spec := sdkr.FlowSpec{
		MaxNodeExecutions: 3,
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinAll},
			{ID: "a1", Tool: "t"}, {ID: "a2", Tool: "t"},
			{ID: "b1", Tool: "t"}, {ID: "b2", Tool: "t"},
			{ID: "c1", Tool: "t"}, {ID: "c2", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a1"}, {From: "fan", To: "b1"}, {From: "fan", To: "c1"},
			{From: "a1", To: "a2"}, {From: "b1", To: "b2"}, {From: "c1", To: "c2"},
		},
		Entry: "fan",
	}
	g := mustCompile(t, spec)
	r := &parRunner{}

	_, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err == nil {
		t.Fatalf("expected the global execution budget to abort the run, executions: %v", r.executed())
	}
	if !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("error = %v, want the budget error", err)
	}
	if n := len(r.executed()); n > 3 {
		t.Fatalf("%d nodes executed under a budget of 3: %v", n, r.executed())
	}
}

func TestRunFlow_ParallelRespectsEdgeIterationBudget(t *testing.T) {
	// A back edge into the fan-out may only be traversed once by default, so the
	// second pass finds no eligible branches instead of looping forever.
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinBestEffort},
			{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{{From: "fan", To: "a"}, {From: "fan", To: "b"}, {From: "a", To: "fan"}},
		Entry: "fan",
	}
	g := mustCompile(t, spec)
	r := &parRunner{}

	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if r.count("a") != 1 || r.count("b") != 1 {
		t.Fatalf("edge budgets must bound the fan-out, executions: %v", r.executed())
	}
}

// ── error policy on the group ────────────────────────────────────────────────

func TestRunFlow_ParallelOnErrorAppliesToTheGroup(t *testing.T) {
	spec := fanOutSpec(sdkr.FlowNode{Join: sdkr.JoinAll, JoinNode: "join", OnError: "skip"}, "a", "b")
	g := mustCompile(t, spec)
	r := &parRunner{errs: map[string]error{"a": errors.New("nope")}}

	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("on_error: skip must swallow the group failure, got %v", err)
	}
	if r.count("join") != 1 {
		t.Fatalf("the walk must continue to the barrier after a skipped group: %v", r.executed())
	}
}

func TestRunFlow_ParallelBranchHonorsNodeLevelOnError(t *testing.T) {
	// A branch node declaring on_error: skip keeps its branch alive, so an "all"
	// join still succeeds — per-branch recovery happens inside the branch walk.
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel},
			{ID: "a", Tool: "t", OnError: "skip"}, {ID: "a2", Tool: "t"},
			{ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"}, {From: "a", To: "a2"},
		},
		Entry: "fan",
	}
	g := mustCompile(t, spec)
	r := &parRunner{errs: map[string]error{"a": errors.New("transient")}}

	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if r.count("a2") != 1 {
		t.Fatalf("the branch must continue past a skipped node: %v", r.executed())
	}
	if want := `[{"node":"a2"},{"node":"b"}]`; string(out) != want {
		t.Fatalf("aggregate=%s, want %s", out, want)
	}
}

func TestRunFlow_NestedParallelWithItsOwnBarrier(t *testing.T) {
	// Nesting is legal as long as each fan-out owns its barrier. The inner group's
	// records must be attributed to the INNER branch, otherwise a nested trace
	// collapses into the outer group and stops being readable.
	g := mustCompile(t, sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "outer", Kind: sdkr.FlowNodeParallel, JoinNode: "outer_join"},
			{ID: "inner", Kind: sdkr.FlowNodeParallel, JoinNode: "inner_join"},
			{ID: "x", Tool: "t"}, {ID: "y", Tool: "t"},
			{ID: "inner_join", Tool: "t"}, {ID: "solo", Tool: "t"},
			{ID: "outer_join", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "outer", To: "inner"}, {From: "outer", To: "solo"},
			{From: "inner", To: "x"}, {From: "inner", To: "y"},
			{From: "x", To: "inner_join"}, {From: "y", To: "inner_join"},
			{From: "inner_join", To: "outer_join"}, {From: "solo", To: "outer_join"},
		},
		Entry: "outer",
	})
	r := &parRunner{}
	rc := &recorder{}

	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, rc.hooks()); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	for _, id := range []string{"x", "y", "inner_join", "solo", "outer_join"} {
		if r.count(id) != 1 {
			t.Fatalf("node %q ran %d times, want 1: %v", id, r.count(id), r.executed())
		}
	}
	inner := rc.byNode("x")[0]
	if inner.ParallelGroup != "inner" || !strings.HasPrefix(inner.BranchID, "inner#1[") {
		t.Fatalf("nested branch must keep its innermost attribution: %+v", inner)
	}
	if got := rc.byNode("outer_join")[0]; got.BranchID != "" {
		t.Fatalf("the outer barrier runs on the main walk: %+v", got)
	}
}

// ── unchanged behavior for graphs with no parallel node ──────────────────────

func TestRunFlow_SequentialGraphStillTakesOneEdge(t *testing.T) {
	g := mustCompile(t, sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "start", Kind: sdkr.FlowNodeBranch},
			{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{{From: "start", To: "a"}, {From: "start", To: "b"}},
		Entry: "start",
	})
	r := &parRunner{}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got := r.executed(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("a plain branch node must still take exactly the first truthy edge, got %v", got)
	}
}
