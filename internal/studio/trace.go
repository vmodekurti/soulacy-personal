// trace.go — structured, durable observability for the Studio build pipeline.
//
// The redesign principle (docs/STUDIO_REDESIGN.md) is "the transcript is the
// UI": a user — and we, debugging — should be able to see EXACTLY what the
// build-verify-repair loop did, in order, with timings, inputs, and outputs,
// without reading server logs or templates. This file is the substrate for that.
//
// A BuildTrace is a single build's ordered event log. Every phase of the loop
// (snapshot → preflight → repair → verify → result) records one structured
// TraceEvent. Events fan out to two sinks: an in-memory mirror (always on, so
// the GUI/report can read the trace back) and an optional JSONL file (so a build
// is fully inspectable after the gateway forgets it — "logs everything").
//
// Two design rules, both load-bearing:
//   - **Nil-safe.** Every BuildTrace method is a no-op on a nil receiver, so the
//     loop can be instrumented unconditionally with `opts.Trace` left nil in
//     tests and lightweight callers — exactly like the loop's nil-safe OnEvent.
//   - **Pure data over injected sinks.** Recorders are a tiny interface, so the
//     whole thing is unit-testable with an in-memory recorder and the JSONL path
//     is a thin, separately-tested adapter.
package studio

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// TraceEvent is one structured, timestamped record of something the build
// pipeline did. It is intentionally flat and JSON-first so it round-trips
// through a JSONL file and renders directly in the GUI.
type TraceEvent struct {
	Seq       int            `json:"seq"`               // monotonic within a trace, 1-based
	At        time.Time      `json:"at"`                // wall-clock (UTC)
	ElapsedMS int64          `json:"elapsed_ms"`        // since the trace started
	DurMS     int64          `json:"dur_ms,omitempty"`  // duration of a timed step, if any
	Kind      string         `json:"kind"`              // phase|snapshot|preflight|repair|verify|result|error
	Phase     string         `json:"phase,omitempty"`   // sub-phase label (e.g. "repair","verify","glue")
	Attempt   int            `json:"attempt,omitempty"` // loop attempt number, 0 outside the loop
	Message   string         `json:"message"`           // plain-language line, ready to show a user
	Data      map[string]any `json:"data,omitempty"`    // structured detail (problems, counts, draft, …)
	Error     string         `json:"error,omitempty"`   // non-empty when this event records a failure
}

// Recorder is the durable sink for trace events. Implementations are an
// in-memory mirror, a JSONL file writer, and a fan-out multi.
type Recorder interface {
	Record(TraceEvent)
	Close() error
}

// memoryRecorder keeps every event in order so the trace can be read back for
// the GUI/report. It is always one of a BuildTrace's sinks.
type memoryRecorder struct {
	mu     sync.Mutex
	events []TraceEvent
}

func (m *memoryRecorder) Record(e TraceEvent) {
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
}

func (m *memoryRecorder) Close() error { return nil }

func (m *memoryRecorder) snapshot() []TraceEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TraceEvent, len(m.events))
	copy(out, m.events)
	return out
}

// jsonlRecorder appends one JSON object per line, flushing each event so a
// crashed or still-running build leaves a complete, tail-able log on disk.
type jsonlRecorder struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func newJSONLRecorder(path string) (*jsonlRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &jsonlRecorder{f: f, w: bufio.NewWriter(f)}, nil
}

func (j *jsonlRecorder) Record(e TraceEvent) {
	b, err := json.Marshal(e)
	if err != nil {
		return // never let a bad event break a build
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = j.w.Write(b)
	_ = j.w.WriteByte('\n')
	_ = j.w.Flush()
}

func (j *jsonlRecorder) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.w.Flush()
	return j.f.Close()
}

type multiRecorder []Recorder

func (m multiRecorder) Record(e TraceEvent) {
	for _, r := range m {
		r.Record(e)
	}
}

func (m multiRecorder) Close() error {
	var first error
	for _, r := range m {
		if err := r.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// BuildTrace is a single build's ordered, durable event log. The zero value is
// not usable; construct via NewMemoryTrace or BuildTraceStore.New. A nil
// *BuildTrace is valid and makes every method a no-op.
type BuildTrace struct {
	ID string
	// WorkspaceID is the tenant that started the build. It is recorded on the
	// trace as well as in the store's key so a JSONL file read back off disk
	// still says whose build it was.
	WorkspaceID string
	Intent      string
	Start       time.Time

	mu   sync.Mutex
	seq  int
	mem  *memoryRecorder // in-memory mirror, always present
	sink Recorder        // mem, optionally fanned out to a JSONL file
}

func newBuildTrace(id string, extra Recorder) *BuildTrace {
	mem := &memoryRecorder{}
	var sink Recorder = mem
	if extra != nil {
		sink = multiRecorder{mem, extra}
	}
	return &BuildTrace{ID: id, Start: time.Now().UTC(), mem: mem, sink: sink}
}

// NewMemoryTrace returns a standalone, in-memory-only trace — for tests and
// callers that want the event mirror without a store or disk file.
func NewMemoryTrace() *BuildTrace { return newBuildTrace(newTraceID(), nil) }

// emit is the single nil-safe write path every public method funnels through.
func (t *BuildTrace) emit(kind, phase string, attempt int, msg string, dur time.Duration, errStr string, data map[string]any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.seq++
	ev := TraceEvent{
		Seq:       t.seq,
		At:        time.Now().UTC(),
		ElapsedMS: time.Since(t.Start).Milliseconds(),
		Kind:      kind,
		Phase:     phase,
		Attempt:   attempt,
		Message:   msg,
		Data:      data,
		Error:     errStr,
	}
	if dur > 0 {
		ev.DurMS = dur.Milliseconds()
	}
	sink := t.sink
	t.mu.Unlock()
	sink.Record(ev)
}

// Log records a plain event.
func (t *BuildTrace) Log(kind, phase string, attempt int, msg string) {
	t.emit(kind, phase, attempt, msg, 0, "", nil)
}

// Logd records an event with structured data.
func (t *BuildTrace) Logd(kind, phase string, attempt int, msg string, data map[string]any) {
	t.emit(kind, phase, attempt, msg, 0, "", data)
}

// Errf records a failure event.
func (t *BuildTrace) Errf(phase string, attempt int, err error, msg string) {
	es := ""
	if err != nil {
		es = err.Error()
	}
	t.emit("error", phase, attempt, msg, 0, es, nil)
}

// Event bridges the loop's existing BuildEvent stream into the trace, so every
// user-facing progress line is also durably recorded with one call.
func (t *BuildTrace) Event(ev BuildEvent) {
	t.emit(ev.Kind, ev.Phase, ev.Attempt, ev.Message, 0, "", nil)
}

// Step starts a timer and returns a closure that records one completion event
// with the elapsed duration, an optional error, and optional structured data.
// Usage:
//
//	done := t.Step("verify", "verify", n, "running the agent")
//	out := run()
//	done(out.err, map[string]any{"ok": out.ok})
func (t *BuildTrace) Step(kind, phase string, attempt int, msg string) func(err error, data map[string]any) {
	if t == nil {
		return func(error, map[string]any) {}
	}
	start := time.Now()
	return func(err error, data map[string]any) {
		es := ""
		if err != nil {
			es = err.Error()
		}
		t.emit(kind, phase, attempt, msg, time.Since(start), es, data)
	}
}

// Snapshot records the shape of a draft at a point in the build: node count,
// ids, kind histogram, a short content hash (so you can see when the draft
// actually changed between attempts), and the full draft for diffing.
func (t *BuildTrace) Snapshot(label string, attempt int, d Draft) {
	if t == nil {
		return
	}
	t.Logd("snapshot", label, attempt, "draft: "+label, draftSnapshot(d))
}

// Events returns a copy of the in-memory event mirror, in order.
func (t *BuildTrace) Events() []TraceEvent {
	if t == nil {
		return nil
	}
	return t.mem.snapshot()
}

// Close flushes and closes the durable sink (the JSONL file, if any). Safe to
// call more than once and on a nil trace.
func (t *BuildTrace) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	sink := t.sink
	t.mu.Unlock()
	return sink.Close()
}

// TraceDump is the serializable form of a trace returned by the gateway.
type TraceDump struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id,omitempty"`
	Intent      string       `json:"intent,omitempty"`
	Start       time.Time    `json:"start"`
	Events      []TraceEvent `json:"events"`
}

// Dump returns the trace header plus its full event list.
func (t *BuildTrace) Dump() TraceDump {
	if t == nil {
		return TraceDump{Events: []TraceEvent{}}
	}
	return TraceDump{ID: t.ID, WorkspaceID: t.WorkspaceID, Intent: t.Intent, Start: t.Start, Events: t.Events()}
}

// draftSnapshot summarizes a draft for a trace event.
func draftSnapshot(d Draft) map[string]any {
	nodes := d.Flow.Nodes
	ids := make([]string, 0, len(nodes))
	kinds := map[string]int{}
	for _, n := range nodes {
		ids = append(ids, n.ID)
		kinds[nodeKind(n)]++
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	out := map[string]any{
		"name":     d.Name,
		"nodes":    len(nodes),
		"node_ids": ids,
		"kinds":    kinds,
		"hash":     hex.EncodeToString(sum[:])[:12],
		"draft":    json.RawMessage(raw),
	}
	if d.Strategy != "" {
		out["strategy"] = d.Strategy
	}
	return out
}

// newTraceID returns a sortable, unique-enough id: a UTC timestamp prefix plus
// a short random suffix.
func newTraceID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("bt-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// ── store ────────────────────────────────────────────────────────────────────

// traceKey identifies a trace by tenant *and* id.
//
// A build trace carries the originating intent — the user's own words — and a
// full snapshot of every draft the loop produced. It is one of the most
// revealing objects in the system, and it used to be reachable by id alone
// from any request. Making the workspace part of the map key means a foreign
// id is a map miss rather than a check someone can forget to write.
type traceKey struct {
	workspaceID string
	id          string
}

// BuildTraceStore keeps the most recent build traces in a bounded, in-memory
// ring (so the GUI can fetch the latest without unbounded growth) and, when a
// directory is configured, also writes each as a JSONL file so a build is fully
// inspectable after the gateway forgets it. Mirrors the proven shape of the
// runtime's flowTraceStore.
//
// The retention cap stays global rather than per workspace, so the memory a
// deployment spends on traces does not grow with the number of tenants. The
// consequence is honest and worth stating: a busy tenant can evict a quiet
// tenant's traces early. That costs a debugging aid, never confidentiality —
// eviction drops traces, it never exposes them.
type BuildTraceStore struct {
	mu  sync.Mutex
	max int
	dir string // "" disables disk persistence
	// byKey is the only lookup path, so a trace cannot be resolved without
	// naming its owner.
	byKey map[traceKey]*BuildTrace
	// perWorkspace holds each tenant's ids oldest-first, so Latest and List
	// read one workspace's traces rather than filtering a shared list.
	perWorkspace map[string][]string
	// eviction is the global retention order, oldest first.
	eviction []traceKey
}

// NewBuildTraceStore returns a store retaining up to max traces (default 50).
// If dir is non-empty it is created best-effort; a directory that cannot be
// created degrades silently to memory-only so observability never breaks a build.
func NewBuildTraceStore(max int, dir string) *BuildTraceStore {
	if max <= 0 {
		max = 50
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			dir = ""
		}
	}
	return &BuildTraceStore{
		max: max, dir: dir,
		byKey:        map[traceKey]*BuildTrace{},
		perWorkspace: map[string][]string{},
	}
}

// Dir reports one workspace's on-disk persistence directory, or "" when
// memory-only. It is surfaced to operators in the traces listing, so it names
// the caller's own directory rather than the shared root.
func (s *BuildTraceStore) Dir(workspaceID string) string {
	if s.dir == "" {
		return ""
	}
	return wsroot.Dir(s.dir, wsroot.Normalize(workspaceID))
}

// New starts and registers a new build trace, opening its JSONL file
// best-effort, evicting the oldest trace past the cap, and recording a start
// event carrying the originating intent.
func (s *BuildTraceStore) New(workspaceID, intent string) *BuildTrace {
	workspaceID = wsroot.Normalize(workspaceID)
	id := newTraceID()
	var extra Recorder
	// The JSONL file is namespaced too: a trace that outlives the ring must
	// not be readable from another tenant's directory either.
	if dir := s.workspaceDir(workspaceID); dir != "" {
		if r, err := newJSONLRecorder(filepath.Join(dir, id+".jsonl")); err == nil {
			extra = r
		}
	}
	t := newBuildTrace(id, extra)
	t.Intent = intent
	t.WorkspaceID = workspaceID

	key := traceKey{workspaceID, id}
	s.mu.Lock()
	s.byKey[key] = t
	s.perWorkspace[workspaceID] = append(s.perWorkspace[workspaceID], id)
	s.eviction = append(s.eviction, key)
	for len(s.eviction) > s.max {
		old := s.eviction[0]
		s.eviction = s.eviction[1:]
		if ot := s.byKey[old]; ot != nil {
			_ = ot.Close()
		}
		delete(s.byKey, old)
		s.perWorkspace[old.workspaceID] = removeID(s.perWorkspace[old.workspaceID], old.id)
		if len(s.perWorkspace[old.workspaceID]) == 0 {
			delete(s.perWorkspace, old.workspaceID)
		}
	}
	s.mu.Unlock()

	t.Logd("phase", "start", 0, "build started", map[string]any{"intent": intent})
	return t
}

// workspaceDir returns the JSONL directory for one workspace, creating it
// best-effort. Disk persistence degrades to memory-only rather than failing a
// build, exactly as it did before.
func (s *BuildTraceStore) workspaceDir(workspaceID string) string {
	if s.dir == "" {
		return ""
	}
	dir := wsroot.Dir(s.dir, workspaceID)
	if dir == s.dir {
		return dir // personal: created at construction
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

func removeID(ids []string, id string) []string {
	for i, candidate := range ids {
		if candidate == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// Get returns the trace for id within one workspace. A trace another workspace
// owns is a map miss, indistinguishable from an id that never existed, so
// trace ids cannot be probed.
func (s *BuildTraceStore) Get(workspaceID, id string) (*BuildTrace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byKey[traceKey{wsroot.Normalize(workspaceID), id}]
	return t, ok
}

// Latest returns the workspace's most recently started trace.
//
// This is the read that mattered most: with no argument it returned whichever
// build was newest across the whole deployment, so a request from one tenant
// could read another tenant's in-flight build intent and drafts.
func (s *BuildTraceStore) Latest(workspaceID string) (*BuildTrace, bool) {
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.perWorkspace[workspaceID]
	if len(ids) == 0 {
		return nil, false
	}
	t, ok := s.byKey[traceKey{workspaceID, ids[len(ids)-1]}]
	return t, ok
}

// TraceSummary is the compact listing form (no event bodies).
type TraceSummary struct {
	ID     string    `json:"id"`
	Intent string    `json:"intent,omitempty"`
	Start  time.Time `json:"start"`
	Events int       `json:"events"`
	Last   string    `json:"last,omitempty"` // last event's message
}

// List returns summaries of one workspace's retained traces, newest first.
func (s *BuildTraceStore) List(workspaceID string) []TraceSummary {
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.perWorkspace[workspaceID]
	out := make([]TraceSummary, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		t := s.byKey[traceKey{workspaceID, ids[i]}]
		if t == nil {
			continue
		}
		evs := t.Events()
		last := ""
		if len(evs) > 0 {
			last = evs[len(evs)-1].Message
		}
		out = append(out, TraceSummary{ID: t.ID, Intent: t.Intent, Start: t.Start, Events: len(evs), Last: last})
	}
	return out
}
