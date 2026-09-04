// flowtrace.go — per-block run trace (Story S0.3 / Phase 1 logging).
//
// As a flow run executes, RunFlow's Observe hook reports one FlowNodeRun per
// executed block (input, output, duration, error). The engine records these in
// a small in-memory, per-agent ring so the GUI can render a legible run trace —
// "which block ran, what it received, what it returned, how long it took, and
// where it failed" — without anyone reading templates or server logs.
//
// In-memory by design for Phase 1: the trace is a debugging/observability aid,
// not durable run history (that lives in checkpoints + the action log). Bounded
// so a long-lived gateway never grows without limit.
package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// FlowRunTrace is the ordered per-block trace of a single flow run.
type FlowRunTrace struct {
	AgentID   string                  `json:"agentId"`
	RunID     string                  `json:"runId"`
	Trigger   string                  `json:"trigger,omitempty"` // source channel: telegram, http, schedule…
	StartedAt time.Time               `json:"startedAt"`
	UpdatedAt time.Time               `json:"updatedAt"`
	Entries   []reasoning.FlowNodeRun `json:"entries"`
}

// FlowRunSummary is a one-line record of a past run for the run-history list —
// EVERY run an agent has done (scheduled or on-demand), with its verdict.
type FlowRunSummary struct {
	RunID     string    `json:"runId"`
	Trigger   string    `json:"trigger,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Steps     int       `json:"steps"`
	Ok        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
}

// flowTraceStore keeps the most recent runs per agent in memory and, when a
// directory is configured, also writes each finished run to disk so the run
// HISTORY survives a gateway restart (durable beyond the in-memory ring).
type flowTraceStore struct {
	mu       sync.Mutex
	maxRuns  int                      // retained runs per agent (in memory)
	dir      string                   // "" disables disk persistence
	diskCap  int                      // retained run files per agent on disk
	byRun    map[string]*FlowRunTrace // runID -> trace
	perAgent map[string][]string      // agentID -> runIDs, oldest first
}

func newFlowTraceStore(maxRuns int, dir string) *flowTraceStore {
	if maxRuns <= 0 {
		maxRuns = 50
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			dir = "" // best-effort: degrade to memory-only, never break a run
		}
	}
	return &flowTraceStore{
		maxRuns:  maxRuns,
		dir:      dir,
		diskCap:  500,
		byRun:    map[string]*FlowRunTrace{},
		perAgent: map[string][]string{},
	}
}

var segSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// agentDir is the on-disk directory holding an agent's run files ("" when disk
// persistence is off).
func (s *flowTraceStore) agentDir(agentID string) string {
	if s.dir == "" {
		return ""
	}
	seg := segSanitizeRe.ReplaceAllString(agentID, "_")
	if seg == "" {
		seg = "_"
	}
	return filepath.Join(s.dir, seg)
}

// persist writes a finished run's full trace to disk and prunes old files. Called
// once when a run completes (best-effort; failures never affect the run).
func (s *flowTraceStore) persist(agentID, runID string) {
	if s.dir == "" || runID == "" {
		return
	}
	s.mu.Lock()
	tr, ok := s.byRun[runID]
	var clone FlowRunTrace
	if ok {
		clone = cloneTrace(tr)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	d := s.agentDir(agentID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return
	}
	b, err := json.Marshal(clone)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(d, segSanitizeRe.ReplaceAllString(runID, "_")+".json"), b, 0o644)
	s.pruneDir(d)
}

// pruneDir keeps at most diskCap run files in d, deleting the oldest by mod time.
func (s *flowTraceStore) pruneDir(d string) {
	ents, err := os.ReadDir(d)
	if err != nil {
		return
	}
	type fe struct {
		name string
		mod  time.Time
	}
	var files []fe
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, fe{e.Name(), info.ModTime()})
		}
	}
	if len(files) <= s.diskCap {
		return
	}
	sort.Slice(files, func(a, b int) bool { return files[a].mod.Before(files[b].mod) })
	for _, f := range files[:len(files)-s.diskCap] {
		_ = os.Remove(filepath.Join(d, f.name))
	}
}

// readDiskTraces loads every persisted run trace for an agent from disk.
func (s *flowTraceStore) readDiskTraces(agentID string) []FlowRunTrace {
	d := s.agentDir(agentID)
	if d == "" {
		return nil
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	var out []FlowRunTrace
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		var tr FlowRunTrace
		if json.Unmarshal(b, &tr) == nil && tr.RunID != "" {
			out = append(out, tr)
		}
	}
	return out
}

// Record appends one block's record to its run's trace, creating the run on
// first sight and evicting the agent's oldest run past the cap.
func (s *flowTraceStore) Record(agentID, runID string, rec reasoning.FlowNodeRun) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, ok := s.byRun[runID]
	if !ok {
		tr = &FlowRunTrace{AgentID: agentID, RunID: runID, StartedAt: rec.StartedAt}
		s.byRun[runID] = tr
		ids := append(s.perAgent[agentID], runID)
		// Evict oldest runs beyond the cap.
		for len(ids) > s.maxRuns {
			old := ids[0]
			ids = ids[1:]
			delete(s.byRun, old)
		}
		s.perAgent[agentID] = ids
	}
	tr.Entries = append(tr.Entries, rec)
	tr.UpdatedAt = time.Now().UTC()
}

// tag records (or pre-creates) a run with its trigger source, so the history can
// show whether a run was scheduled or on-demand. Called once at run start, before
// any node executes.
func (s *flowTraceStore) tag(agentID, runID, trigger string) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, ok := s.byRun[runID]
	if !ok {
		tr = &FlowRunTrace{AgentID: agentID, RunID: runID, StartedAt: time.Now().UTC()}
		s.byRun[runID] = tr
		ids := append(s.perAgent[agentID], runID)
		for len(ids) > s.maxRuns {
			old := ids[0]
			ids = ids[1:]
			delete(s.byRun, old)
		}
		s.perAgent[agentID] = ids
	}
	if trigger != "" {
		tr.Trigger = trigger
	}
}

// summarize reduces a trace to its history row.
func summarize(tr FlowRunTrace) FlowRunSummary {
	sum := FlowRunSummary{
		RunID: tr.RunID, Trigger: tr.Trigger,
		StartedAt: tr.StartedAt, UpdatedAt: tr.UpdatedAt,
		Steps: len(tr.Entries), Ok: true,
	}
	for _, e := range tr.Entries {
		if e.Error != "" {
			sum.Ok = false
			sum.Error = e.Error
		}
	}
	return sum
}

// list returns a summary of every retained run for an agent, newest first — the
// complete run history regardless of trigger. It merges the in-memory ring with
// the durable on-disk runs (in-memory wins for a given runId), so history
// survives a gateway restart.
func (s *flowTraceStore) list(agentID string) []FlowRunSummary {
	byID := map[string]FlowRunSummary{}
	orderByID := map[string]int{}

	s.mu.Lock()
	for idx, id := range s.perAgent[agentID] {
		if tr := s.byRun[id]; tr != nil {
			byID[id] = summarize(*tr)
			orderByID[id] = idx
		}
	}
	s.mu.Unlock()

	for _, tr := range s.readDiskTraces(agentID) {
		if _, have := byID[tr.RunID]; !have {
			byID[tr.RunID] = summarize(tr)
			orderByID[tr.RunID] = -1
		}
	}

	type row struct {
		sum   FlowRunSummary
		order int
	}
	rows := make([]row, 0, len(byID))
	for _, sum := range byID {
		rows = append(rows, row{sum: sum, order: orderByID[sum.RunID]})
	}
	sort.Slice(rows, func(a, b int) bool {
		at := rows[a].sum.UpdatedAt
		if at.IsZero() {
			at = rows[a].sum.StartedAt
		}
		bt := rows[b].sum.UpdatedAt
		if bt.IsZero() {
			bt = rows[b].sum.StartedAt
		}
		if !at.Equal(bt) {
			return at.After(bt)
		}
		if rows[a].order != rows[b].order {
			return rows[a].order > rows[b].order
		}
		return rows[a].sum.RunID > rows[b].sum.RunID
	})
	out := make([]FlowRunSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.sum)
	}
	return out
}

// getFrom returns a run's trace, falling back to disk when it's no longer in the
// in-memory ring (so an old run from the history is still viewable).
func (s *flowTraceStore) getFrom(agentID, runID string) (FlowRunTrace, bool) {
	if tr, ok := s.get(runID); ok {
		return tr, true
	}
	for _, tr := range s.readDiskTraces(agentID) {
		if tr.RunID == runID {
			return tr, true
		}
	}
	return FlowRunTrace{}, false
}

// get returns a deep-enough copy of the trace for runID.
func (s *flowTraceStore) get(runID string) (FlowRunTrace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, ok := s.byRun[runID]
	if !ok {
		return FlowRunTrace{}, false
	}
	return cloneTrace(tr), true
}

// latest returns the agent's most recent run trace.
func (s *flowTraceStore) latest(agentID string) (FlowRunTrace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.perAgent[agentID]
	if len(ids) == 0 {
		return FlowRunTrace{}, false
	}
	tr, ok := s.byRun[ids[len(ids)-1]]
	if !ok {
		return FlowRunTrace{}, false
	}
	return cloneTrace(tr), true
}

func cloneTrace(tr *FlowRunTrace) FlowRunTrace {
	out := *tr
	out.Entries = append([]reasoning.FlowNodeRun(nil), tr.Entries...)
	return out
}

// ftStore lazily initializes and returns the engine's flow-trace store.
func (e *Engine) ftStore() *flowTraceStore {
	e.flowTraceOnce.Do(func() { e.flowTraces = newFlowTraceStore(50, flowRunsDir()) })
	return e.flowTraces
}

// recordFlowNode adds one block's record to the agent's current run trace.
func (e *Engine) recordFlowNode(agentID, runID string, rec reasoning.FlowNodeRun) {
	e.ftStore().Record(agentID, runID, rec)
}

// FlowTrace returns the per-block trace for a specific run id.
func (e *Engine) FlowTrace(runID string) (FlowRunTrace, bool) {
	return e.ftStore().get(runID)
}

// LatestFlowTrace returns an agent's most recent flow run trace.
func (e *Engine) LatestFlowTrace(agentID string) (FlowRunTrace, bool) {
	return e.ftStore().latest(agentID)
}

// TagFlowRun records a run's trigger source (channel) at run start.
func (e *Engine) TagFlowRun(agentID, runID, trigger string) {
	e.ftStore().tag(agentID, runID, trigger)
}

// FlowRunHistory returns a summary of every retained run for an agent, newest
// first — scheduled and on-demand alike, merged from memory and durable disk.
func (e *Engine) FlowRunHistory(agentID string) []FlowRunSummary {
	return e.ftStore().list(agentID)
}

// PersistFlowRun durably writes a finished run's trace to disk (best-effort).
func (e *Engine) PersistFlowRun(agentID, runID string) {
	e.ftStore().persist(agentID, runID)
}

// FlowTraceFor returns a run's trace by agent + run id, falling back to disk so a
// run that has aged out of the in-memory ring is still viewable from the history.
func (e *Engine) FlowTraceFor(agentID, runID string) (FlowRunTrace, bool) {
	return e.ftStore().getFrom(agentID, runID)
}

// flowRunsDir resolves the durable run-history directory: SOULACY_STUDIO_RUNS_DIR
// if set, else <workspace>/studio-runs, else <home>/.soulacy/studio-runs. Empty
// (memory-only) only if no writable location can be determined.
func flowRunsDir() string {
	if d := os.Getenv("SOULACY_STUDIO_RUNS_DIR"); d != "" {
		return d
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-runs")
}
