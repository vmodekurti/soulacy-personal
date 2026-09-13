package safeundo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	mu          sync.Mutex
	server      *httptest.Server
	record      map[string]any
	text        string
	textMedia   string
	versions    map[string]int
	writes      map[string]int
	gets        map[string]int
	weak        bool
	failPath    string
	failStatus  int
	afterWrite  func(string)
	beforeWrite func(string)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{record: map[string]any{"status": "new", "note": "original", "nullable": nil, "a/b~c": "old"}, text: "Original document", textMedia: "text/plain", versions: map[string]int{"/record": 1, "/document": 1}, writes: map[string]int{}, gets: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if r.Method == "GET" {
		f.gets[path]++
		tag := fmt.Sprintf(`"v%d"`, f.versions[path])
		if f.weak {
			tag = "W/" + tag
		}
		w.Header().Set("ETag", tag)
		if path == "/record" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.record)
		} else {
			w.Header().Set("Content-Type", f.textMedia)
			io.WriteString(w, f.text)
		}
		return
	}
	if f.beforeWrite != nil {
		f.beforeWrite(path)
	}
	if r.Header.Get("If-Match") != fmt.Sprintf(`"v%d"`, f.versions[path]) {
		w.WriteHeader(412)
		return
	}
	if path == f.failPath && f.failStatus < 500 {
		w.WriteHeader(f.failStatus)
		return
	}
	if path == "/record" {
		if r.Method != "PATCH" || r.Header.Get("Content-Type") != "application/json-patch+json" {
			w.WriteHeader(405)
			return
		}
		var ops []struct {
			Op, Path string
			Value    json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&ops) != nil {
			w.WriteHeader(422)
			return
		}
		// Apply to a copy: JSON Patch is atomic when an operation fails.
		copy := map[string]any{}
		for k, v := range f.record {
			copy[k] = v
		}
		for _, op := range ops {
			key := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(op.Path, "/"), "~1", "/"), "~0", "~")
			var value any
			if len(op.Value) > 0 {
				d := json.NewDecoder(strings.NewReader(string(op.Value)))
				d.UseNumber()
				if d.Decode(&value) != nil {
					w.WriteHeader(422)
					return
				}
			}
			switch op.Op {
			case "test":
				want, _ := json.Marshal(value)
				if !equalValue(fieldValue(copy, key), Value{Exists: true, JSON: want}) {
					w.WriteHeader(409)
					return
				}
			case "add", "replace":
				copy[key] = value
			case "remove":
				delete(copy, key)
			default:
				w.WriteHeader(422)
				return
			}
		}
		f.record = copy
	} else {
		if r.Method != "PUT" || r.Header.Get("Content-Type") != f.textMedia {
			w.WriteHeader(405)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.text = string(b)
	}
	f.writes[path]++
	f.versions[path]++
	if f.afterWrite != nil {
		f.afterWrite(path)
	}
	if path == f.failPath {
		w.WriteHeader(f.failStatus)
		return
	}
	w.WriteHeader(204)
}

func (f *fixture) config() Config {
	return Config{Resources: []Resource{
		{ID: "record", AgentID: "agent", Name: "Customer", Kind: "json_record", URL: f.server.URL + "/record", Fields: []string{"status", "note", "nullable", "a/b~c", "added"}, ConditionalWrites: true, AllowLoopbackHTTP: true},
		{ID: "document", AgentID: "agent", Name: "Document", Kind: "webdav_text", URL: f.server.URL + "/document", ConditionalWrites: true, AllowLoopbackHTTP: true},
	}}
}
func openTestStore(t *testing.T, cfg Config) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "undo.db")
	s, err := NewStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func allow() error         { return nil }
func str(s string) *string { return &s }
func changes() PrepareRequest {
	return PrepareRequest{Title: "Update customer and document", Changes: []ChangeInput{
		{ResourceID: "record", Fields: map[string]json.RawMessage{"status": json.RawMessage(`"active"`)}},
		{ResourceID: "document", Text: str("Updated document")},
	}}
}
func prepare(t *testing.T, s *Store, input PrepareRequest) Job {
	t.Helper()
	j, err := s.Prepare(context.Background(), "owner", "agent", "run", input, allow)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func review(t *testing.T, s *Store, j Job, direction string) Review {
	t.Helper()
	r, err := s.Preview(context.Background(), "owner", "agent", j.ID, direction, allow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func execute(t *testing.T, s *Store, r Review) Job {
	t.Helper()
	j, err := s.Execute(context.Background(), "owner", "agent", r.Job.ID, r.Token, allow)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSafeUndoCrossSystemAndIdempotency(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	j := prepare(t, s, changes())
	if len(f.writes) != 0 || j.Status != "draft" {
		t.Fatal("prepare wrote to external systems")
	}
	r := review(t, s, j, "apply")
	j = execute(t, s, r)
	if j.Status != "applied" || f.text != "Updated document" || f.record["status"] != "active" {
		t.Fatalf("apply = %+v", j)
	}
	for range 5 {
		execute(t, s, r)
	}
	if f.writes["/record"] != 1 || f.writes["/document"] != 1 {
		t.Fatal("duplicate delivery replayed an action")
	}
	f.mu.Lock()
	f.record["note"] = "A teammate's newer note"
	f.versions["/record"]++
	f.mu.Unlock()
	undo := review(t, s, j, "undo")
	j = execute(t, s, undo)
	if j.Status != "undone" || f.record["status"] != "new" || f.record["note"] != "A teammate's newer note" || f.text != "Original document" {
		t.Fatalf("unsafe undo: %+v, %v", j, f.record)
	}
	if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "apply", allow); !errors.Is(err, ErrConflict) {
		t.Fatalf("reapplied undone job: %v", err)
	}
	public, _ := json.Marshal(j)
	if strings.Contains(string(public), "binding") || strings.Contains(string(public), r.Token) || strings.Contains(string(public), f.server.URL) {
		t.Fatal("private receipt fields leaked")
	}
}

func TestSafeUndoFreshPreviewAndConflicts(t *testing.T) {
	for _, scenario := range []string{"stale-preview", "field-conflict", "document-ABA", "write-race"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			s, _ := openTestStore(t, f.config())
			j := prepare(t, s, changes())
			r := review(t, s, j, "apply")
			switch scenario {
			case "stale-preview":
				f.record["note"] = "newer"
				f.versions["/record"]++
				if _, err := s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, allow); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			case "field-conflict":
				j = execute(t, s, r)
				f.record["status"] = "human decision"
				f.versions["/record"]++
				if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "undo", allow); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			case "document-ABA":
				j = execute(t, s, r)
				f.versions["/document"] += 2
				if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "undo", allow); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			case "write-race":
				f.beforeWrite = func(path string) { f.versions[path]++; f.beforeWrite = nil }
				j = execute(t, s, r)
				if j.Status != "draft" || j.Notice == "" {
					t.Fatalf("race = %+v", j)
				}
			}
			if scenario == "stale-preview" || scenario == "write-race" {
				if len(f.writes) != 0 {
					t.Fatal("stale state was overwritten")
				}
			}
		})
	}
}

func TestSafeUndoUnknownResultRequiresExplicitReconciliation(t *testing.T) {
	f := newFixture(t)
	s, path := openTestStore(t, f.config())
	f.failPath, f.failStatus = "/record", 500
	j := execute(t, s, review(t, s, prepare(t, s, changes()), "apply"))
	if j.Status != "needs_review" || f.writes["/record"] != 1 || f.writes["/document"] != 0 {
		t.Fatalf("lost ack = %+v", j)
	}
	if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "apply", allow); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	s.Close()
	s, err := NewStore(path, f.config())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.Get(context.Background(), "owner", "agent", j.ID)
	if got.Status != "needs_review" {
		t.Fatal("unknown write was reset")
	}
	r := review(t, s, got, "reconcile")
	if !strings.Contains(r.Warning, "does not prove") {
		t.Fatal("reconciliation overclaims proof")
	}
	j = execute(t, s, r)
	if j.Status != "partial" || !j.Actions[0].Reconciled || f.writes["/record"] != 1 {
		t.Fatalf("reconcile = %+v", j)
	}
	f.failPath = ""
	j = execute(t, s, review(t, s, j, "apply"))
	if j.Status != "applied" || f.writes["/record"] != 1 {
		t.Fatal("recovery repeated completed action")
	}
	j = execute(t, s, review(t, s, j, "undo"))
	if j.Status != "undone" {
		t.Fatal(j.Status)
	}
}

func TestSafeUndoCrashQuarantineAndKnownProgress(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			f := newFixture(t)
			s, path := openTestStore(t, f.config())
			j := prepare(t, s, changes())
			private, _ := s.get(context.Background(), "owner", "agent", j.ID)
			private.Actions[0].Status = "running"
			private.Actions[0].PriorStatus = "pending"
			if err := s.persist(&private); err != nil {
				t.Fatal(err)
			}
			if effect {
				f.record["status"] = "active"
				f.versions["/record"]++
			}
			s.Close()
			s, err := NewStore(path, f.config())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			j, _ = s.Get(context.Background(), "owner", "agent", j.ID)
			if j.Status != "needs_review" || len(f.writes) != 0 {
				t.Fatal("interrupted action was replayed")
			}
			j = execute(t, s, review(t, s, j, "reconcile"))
			want := "draft"
			if effect {
				want = "partial"
			}
			if j.Status != want {
				t.Fatalf("%s != %s", j.Status, want)
			}
		})
	}
}

func TestSafeUndoPermissionsConcurrencyAndOwner(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	j := prepare(t, s, changes())
	r := review(t, s, j, "apply")
	if _, err := s.Get(context.Background(), "other", "agent", j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, nil); !errors.Is(err, ErrPermission) {
		t.Fatal(err)
	}
	var allowed atomic.Bool
	allowed.Store(true)
	f.afterWrite = func(string) { allowed.Store(false) }
	authority := func() error {
		if !allowed.Load() {
			return ErrPermission
		}
		return nil
	}
	j, err := s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, authority)
	if err != nil || j.Status != "partial" || f.writes["/document"] != 0 {
		t.Fatalf("revocation = %+v %v", j, err)
	}
	f.afterWrite = nil
	r = review(t, s, j, "apply")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, allow)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.writes["/document"] != 1 || f.writes["/record"] != 1 {
		t.Fatal("concurrent execute replayed changes")
	}
}

func TestSafeUndoTrackedDependenciesAndUnknownResourceLock(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	one := changes()
	one.Changes = one.Changes[:1]
	j1 := execute(t, s, review(t, s, prepare(t, s, one), "apply"))
	one.Changes[0].Fields["status"] = json.RawMessage(`"later"`)
	j2 := execute(t, s, review(t, s, prepare(t, s, one), "apply"))
	if _, err := s.Preview(context.Background(), "owner", "agent", j1.ID, "undo", allow); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	execute(t, s, review(t, s, j2, "undo"))
	execute(t, s, review(t, s, j1, "undo"))
	f.failPath, f.failStatus = "/record", 500
	one.Changes[0].Fields["status"] = json.RawMessage(`"unknown"`)
	execute(t, s, review(t, s, prepare(t, s, one), "apply"))
	one.Changes[0].Fields["status"] = json.RawMessage(`"next"`)
	blocked := prepare(t, s, one)
	if _, err := s.Preview(context.Background(), "owner", "agent", blocked.ID, "apply", allow); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestSafeUndoJSONNullMissingEscapesAndLargeNumbers(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	input := PrepareRequest{Title: "Exact JSON", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"a/b~c": json.RawMessage(`9007199254740993`), "added": json.RawMessage(`null`)}, Remove: []string{"nullable"}}}}
	j := execute(t, s, review(t, s, prepare(t, s, input), "apply"))
	if _, ok := f.record["nullable"]; ok {
		t.Fatal("remove became null")
	}
	if _, ok := f.record["added"]; !ok {
		t.Fatal("null became missing")
	}
	if fmt.Sprint(f.record["a/b~c"]) != "9007199254740993" {
		t.Fatal("number lost precision")
	}
	execute(t, s, review(t, s, j, "undo"))
	if _, ok := f.record["nullable"]; !ok {
		t.Fatal("null not restored")
	}
	if _, ok := f.record["added"]; ok {
		t.Fatal("missing not restored")
	}
}

func TestSafeUndoLimitsExpiryBindingAndExclusiveLedger(t *testing.T) {
	f := newFixture(t)
	s, path := openTestStore(t, f.config())
	j := prepare(t, s, changes())
	r := review(t, s, j, "apply")
	if second, err := NewStore(path, f.config()); err == nil {
		second.Close()
		t.Fatal("two processes own recovery")
	}
	var syncMode int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&syncMode); err != nil || syncMode != 2 {
		t.Fatalf("ledger not fully durable: %d %v", syncMode, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("ledger has broad permissions")
	}
	s.now = func() time.Time { return r.ExpiresAt }
	if _, err := s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, allow); !errors.Is(err, ErrConflict) {
		t.Fatal("expired preview accepted", err)
	}
	s.Close()
	cfg := f.config()
	cfg.Resources[0].Fields = []string{"status"}
	s, err := NewStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "apply", allow); !errors.Is(err, ErrPermission) {
		t.Fatal("configuration change retained authority", err)
	}
}

func TestSafeUndoMalformedInputAndWeakVersions(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"x":{"a":1,"a":2}}`, `[1] garbage`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34), "{", string([]byte{0xff})} {
		if _, err := parseJSON([]byte(raw)); err == nil {
			t.Errorf("accepted ambiguous JSON: %.80s", raw)
		}
	}
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	f.weak = true
	if _, err := s.Prepare(context.Background(), "owner", "agent", "", changes(), allow); !errors.Is(err, ErrUnavailable) {
		t.Fatal("weak version accepted", err)
	}
	f.weak = false
	for _, input := range []PrepareRequest{
		{Title: "empty"}, {Title: "bad", Changes: []ChangeInput{{ResourceID: "not-configured", Text: str("x")}}},
		{Title: "duplicate", Changes: []ChangeInput{changes().Changes[0], changes().Changes[0]}},
		{Title: "big", Changes: []ChangeInput{{ResourceID: "document", Text: str(strings.Repeat("x", MaxBody+1))}}},
		{Title: "field", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"password": json.RawMessage(`"x"`)}}}},
	} {
		if _, err := s.Prepare(context.Background(), "owner", "agent", "", input, allow); err == nil {
			t.Errorf("accepted %+v", input.Title)
		}
	}
	if len(f.writes) != 0 {
		t.Fatal("invalid input caused writes")
	}
}

func FuzzSafeUndoJSON(f *testing.F) {
	for _, seed := range []string{`{"status":"new"}`, `null`, `[1,2]`, `{"a":1,"a":2}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, err := parseJSON(raw)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseJSON(encoded); err != nil {
			t.Fatal("canonical roundtrip failed", err)
		}
	})
}
