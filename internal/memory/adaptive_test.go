package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func newFactStore(t *testing.T) *FactSQLite {
	t.Helper()
	s, err := OpenFactSQLite(filepath.Join(t.TempDir(), "adaptive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// wordEmbedder is a deterministic bag-of-words embedder so cosine similarity
// is meaningful in tests without a model.
type wordEmbedder struct{ vocab []string }

func (w *wordEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, len(w.vocab))
	for _, tok := range Tokenize(text) {
		for i, v := range w.vocab {
			if tok == v {
				vec[i] += 1
			}
		}
	}
	return vec, nil
}

// scriptedCompleter returns canned replies in order and records prompts.
type scriptedCompleter struct {
	mu      sync.Mutex
	replies []string
	calls   []string
}

func (s *scriptedCompleter) fn(_ context.Context, system, user string, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, system+"\n"+user)
	if len(s.replies) == 0 {
		return "[]", nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func candidatesJSON(cs ...Candidate) string {
	b, _ := json.Marshal(cs)
	return string(b)
}

var scope = FactScope{Owner: "ada", AgentID: "helper"}

// ── E27: extraction ─────────────────────────────────────────────────────────

func TestExtractionPromptStaysUnder200Tokens(t *testing.T) {
	long := strings.Repeat("I prefer concise answers and I live in Denver with my dog Rex. ", 20)
	system, user := ExtractionPrompt(Turn{User: long, Assistant: strings.Repeat("Sure, noted. ", 40)}, ExtractOptions{})
	if got := EstimateTokens(system + "\n" + user); got >= 200 {
		t.Fatalf("extraction prompt estimated at %d tokens, want < 200", got)
	}
}

func TestCasualTurnsProduceNoCandidatesAndNoWrites(t *testing.T) {
	store := newFactStore(t)
	sc := &scriptedCompleter{replies: []string{`[{"fact":"should never be stored","category":"identity","confidence":0.9}]`}}
	eng := NewLocalAdaptive(store, nil, sc.fn, LocalOptions{})
	for _, text := range []string{"thanks", "hello!", "ok", "yes", "👍", "hi there"} {
		out, err := eng.Remember(context.Background(), scope, Turn{User: text})
		if err != nil {
			t.Fatal(err)
		}
		if out.Candidates != 0 || len(out.Added) != 0 {
			t.Fatalf("casual turn %q produced candidates: %+v", text, out)
		}
	}
	if len(sc.calls) != 0 {
		t.Fatalf("casual turns must not call the model, got %d calls", len(sc.calls))
	}
	facts, _ := store.List(context.Background(), scope, "", 0)
	if len(facts) != 0 {
		t.Fatalf("expected no database mutation, found %d facts", len(facts))
	}
}

func TestParseCandidatesTolerantAndStrict(t *testing.T) {
	raw := "Here you go:\n```json\n[{\"fact\":\"User prefers metric units\",\"category\":\"Preference\",\"confidence\":0.8},{\"fact\":\"\",\"category\":\"identity\"},{\"fact\":\"User prefers metric units\",\"category\":\"preference\"},{\"fact\":\"nonsense\",\"category\":\"other\"}]\n```"
	got := ParseCandidates(raw)
	if len(got) != 1 || got[0].Category != "preference" || got[0].Fact != "User prefers metric units" {
		t.Fatalf("unexpected candidates: %+v", got)
	}
	if ParseCandidates("not json") != nil || ParseCandidates("[]") != nil {
		t.Fatal("garbage or empty output must yield no candidates")
	}
}

// ── E28: resolution ─────────────────────────────────────────────────────────

func TestNewFactSupersedesConflictingOldFact(t *testing.T) {
	store := newFactStore(t)
	emb := &wordEmbedder{vocab: []string{"lives", "san", "francisco", "new", "york", "dog", "rex", "metric"}}
	sc := &scriptedCompleter{replies: []string{
		candidatesJSON(Candidate{Fact: "User lives in San Francisco", Category: "identity", Confidence: 0.9}),
		candidatesJSON(Candidate{Fact: "User lives in New York", Category: "identity", Confidence: 0.9}),
		`{"decision":"supersede"}`,
	}}
	eng := NewLocalAdaptive(store, emb, sc.fn, LocalOptions{SimilarityThreshold: 0.3})
	ctx := context.Background()
	if _, err := eng.Remember(ctx, scope, Turn{User: "I live in San Francisco these days"}); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Remember(ctx, scope, Turn{User: "Update: I moved, I live in New York now"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Superseded) != 1 || len(out.Added) != 1 {
		t.Fatalf("expected one supersede, got %+v", out)
	}
	active, _ := store.List(ctx, scope, FactStatusActive, 0)
	if len(active) != 1 || active[0].Content != "User lives in New York" {
		t.Fatalf("active facts wrong: %+v", active)
	}
	old, err := store.Get(ctx, scope, out.Superseded[0])
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != FactStatusSuperseded || old.SupersededBy != active[0].ID {
		t.Fatalf("audit trail missing: %+v", old)
	}
	if active[0].Supersedes != old.ID {
		t.Fatalf("replacement should point back at the old fact: %+v", active[0])
	}
}

func TestDuplicateFactIsNoopAndComplementIsAdded(t *testing.T) {
	store := newFactStore(t)
	emb := &wordEmbedder{vocab: []string{"prefers", "metric", "units", "dog", "rex"}}
	sc := &scriptedCompleter{replies: []string{
		candidatesJSON(Candidate{Fact: "User prefers metric units", Category: "preference", Confidence: 0.9}),
		// identical restatement → noop without consulting the model
		candidatesJSON(Candidate{Fact: "User prefers metric units", Category: "preference", Confidence: 0.9}),
		// unrelated → complement, no arbitration call needed
		candidatesJSON(Candidate{Fact: "User has a dog named Rex", Category: "entity", Confidence: 0.7}),
	}}
	eng := NewLocalAdaptive(store, emb, sc.fn, LocalOptions{})
	ctx := context.Background()
	for _, text := range []string{"Please use metric units for everything", "I said metric units please", "My dog Rex loves walks"} {
		if _, err := eng.Remember(ctx, scope, Turn{User: text}); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := store.List(ctx, scope, FactStatusActive, 0)
	if len(active) != 2 {
		t.Fatalf("expected 2 active facts (metric + dog), got %d: %+v", len(active), active)
	}
	if len(sc.calls) != 3 {
		t.Fatalf("expected exactly 3 extraction calls and no arbitration, got %d", len(sc.calls))
	}
}

func TestLowConfidenceCandidatesAreSkipped(t *testing.T) {
	store := newFactStore(t)
	sc := &scriptedCompleter{replies: []string{candidatesJSON(Candidate{Fact: "User might like jazz", Category: "preference", Confidence: 0.2})}}
	eng := NewLocalAdaptive(store, nil, sc.fn, LocalOptions{MinConfidence: 0.5})
	out, _ := eng.Remember(context.Background(), scope, Turn{User: "maybe I like jazz, not sure honestly"})
	if out.Skipped != 1 || len(out.Added) != 0 {
		t.Fatalf("low confidence should be skipped: %+v", out)
	}
}

// ── E29: retrieval ──────────────────────────────────────────────────────────

func TestRecallIsScopedToOwnerWorkspaceAndAgent(t *testing.T) {
	store := newFactStore(t)
	eng := NewLocalAdaptive(store, nil, nil, LocalOptions{})
	ctx := context.Background()
	mine := scope
	otherOwner := FactScope{Owner: "bob", AgentID: "helper"}
	otherWorkspace := FactScope{Workspace: "acme", Owner: "ada", AgentID: "helper"}
	otherAgent := FactScope{Owner: "ada", AgentID: "planner"}
	for _, sc := range []FactScope{mine, otherOwner, otherWorkspace, otherAgent} {
		if _, err := eng.Add(ctx, sc, FactInput{Category: FactIdentity, Content: "User lives in Denver near the mountains"}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := eng.Recall(ctx, mine, "where do I live? Denver mountains", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Owner != "ada" || hits[0].AgentID != "helper" || hits[0].Workspace != DefaultWorkspace {
		t.Fatalf("recall leaked across tenants: %+v", hits)
	}
	// Cross-agent view for one owner (agent empty) still never crosses owners.
	all, _ := eng.List(ctx, FactScope{Owner: "ada"}, "", 0)
	if len(all) != 2 {
		t.Fatalf("owner-wide list should see 2 agents' facts, got %d", len(all))
	}
}

func TestPromptBlockFormatAndTokenBudget(t *testing.T) {
	var facts []ScoredFact
	for i := 0; i < 12; i++ {
		facts = append(facts, ScoredFact{Fact: Fact{Status: FactStatusActive, Content: "User prefers concise, direct answers with examples and no filler text"}, Score: 1})
	}
	block := FormatPromptBlock(facts, 50)
	if !strings.HasPrefix(block, PromptBlockHeader+"\n- ") {
		t.Fatalf("block missing header/bullets: %q", block)
	}
	if got := EstimateTokens(strings.TrimPrefix(block, PromptBlockHeader)); got > 50 {
		t.Fatalf("block body estimated at %d tokens, want <= 50", got)
	}
	if FormatPromptBlock(nil, 50) != "" {
		t.Fatal("empty facts must yield no block")
	}
}

func TestHybridRankingPrefersKeywordMatchWhenVectorsTie(t *testing.T) {
	facts := []Fact{
		{ID: "a", Status: FactStatusActive, Content: "User prefers dark mode in editors"},
		{ID: "b", Status: FactStatusActive, Content: "User has a dog named Rex"},
	}
	got := RankFacts(facts, nil, Tokenize("tell me about my dog Rex"), 2)
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("keyword half of hybrid search should rank the dog fact first: %+v", got)
	}
}

// ── store CRUD / purge / export shape ──────────────────────────────────────

func TestManualAddUpdateDeletePurge(t *testing.T) {
	store := newFactStore(t)
	eng := NewLocalAdaptive(store, nil, nil, LocalOptions{})
	ctx := context.Background()
	f, err := eng.Add(ctx, scope, FactInput{Category: FactConstraint, Content: "  Never email the user   before 9am "})
	if err != nil {
		t.Fatal(err)
	}
	if f.Content != "Never email the user before 9am" || f.Source != "manual" {
		t.Fatalf("normalisation failed: %+v", f)
	}
	if _, err := eng.Add(ctx, scope, FactInput{Category: "bogus", Content: "x"}); err == nil {
		t.Fatal("invalid category must be rejected")
	}
	if _, err := eng.Add(ctx, FactScope{AgentID: "helper"}, FactInput{Category: FactIdentity, Content: "no owner"}); err == nil {
		t.Fatal("missing owner must be rejected")
	}
	u, err := eng.Update(ctx, scope, f.ID, FactInput{Category: FactConstraint, Content: "Never email the user before 10am"})
	if err != nil || u.Content != "Never email the user before 10am" {
		t.Fatalf("update failed: %v %+v", err, u)
	}
	if _, err := eng.Update(ctx, FactScope{Owner: "bob", AgentID: "helper"}, f.ID, FactInput{Category: FactConstraint, Content: "hijack"}); err == nil {
		t.Fatal("another owner must not be able to edit the fact")
	}
	if err := eng.Delete(ctx, FactScope{Owner: "bob", AgentID: "helper"}, f.ID); err == nil {
		t.Fatal("another owner must not be able to delete the fact")
	}
	if _, err := eng.Add(ctx, scope, FactInput{Category: FactPreference, Content: "Likes tea"}); err != nil {
		t.Fatal(err)
	}
	n, err := eng.Purge(ctx, scope)
	if err != nil || n != 2 {
		t.Fatalf("purge removed %d (%v), want 2", n, err)
	}
	left, _ := eng.List(ctx, scope, "", 0)
	if len(left) != 0 {
		t.Fatalf("purge left %d facts", len(left))
	}
}

// ── E31: Mem0 adapter ──────────────────────────────────────────────────────

func TestMem0AdapterMapsScopeAndEvents(t *testing.T) {
	var gotAdd map[string]any
	var gotSearch map[string]any
	var gotDeleteQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/memories":
			_ = json.NewDecoder(r.Body).Decode(&gotAdd)
			_, _ = w.Write([]byte(`{"results":[{"id":"m1","memory":"User lives in New York","event":"UPDATE"},{"id":"m2","memory":"User has a dog","event":"ADD"},{"id":"m3","event":"NONE"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/search":
			_ = json.NewDecoder(r.Body).Decode(&gotSearch)
			_, _ = w.Write([]byte(`[{"id":"m1","memory":"User lives in New York","score":0.91,"metadata":{"workspace":"default","category":"identity"}},{"id":"zz","memory":"other workspace","score":0.99,"metadata":{"workspace":"acme"}}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/memories":
			_, _ = w.Write([]byte(`{"memories":[{"id":"m1","memory":"User lives in New York","created_at":"2026-09-13T10:00:00Z"}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/memories":
			gotDeleteQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/memories/m1":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	m, err := NewMem0Adaptive(Mem0Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if m.Style() != "server" || m.Provider() != "mem0" {
		t.Fatalf("unexpected adapter identity: %s %s", m.Style(), m.Provider())
	}
	ctx := context.Background()
	out, err := m.Remember(ctx, scope, Turn{SessionID: "s1", User: "I moved to New York and got a dog", Assistant: "Congrats!"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAdd["user_id"] != "ada" || gotAdd["agent_id"] != "helper" {
		t.Fatalf("scope not mapped to mem0 ids: %+v", gotAdd)
	}
	if len(out.Added) != 2 || len(out.Superseded) != 1 || out.Skipped != 1 {
		t.Fatalf("events not mapped: %+v", out)
	}
	hits, err := m.Recall(ctx, scope, "where do I live", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" || hits[0].Category != FactIdentity {
		t.Fatalf("recall should filter by workspace and map categories: %+v", hits)
	}
	if gotSearch["query"] != "where do I live" || gotSearch["user_id"] != "ada" {
		t.Fatalf("search body wrong: %+v", gotSearch)
	}
	facts, err := m.List(ctx, scope, FactStatusActive, 0)
	if err != nil || len(facts) != 1 || facts[0].Source != "provider" {
		t.Fatalf("list failed: %v %+v", err, facts)
	}
	if err := m.Delete(ctx, scope, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Purge(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotDeleteQuery, "user_id=ada") || !strings.Contains(gotDeleteQuery, "agent_id=helper") {
		t.Fatalf("purge must be scoped: %q", gotDeleteQuery)
	}
}

func TestMem0AdapterRejectsPlatformWithoutKeyAndBadURL(t *testing.T) {
	if _, err := NewMem0Adaptive(Mem0Config{BaseURL: "https://api.mem0.ai"}); err == nil {
		t.Fatal("hosted platform without api_key must be rejected")
	}
	if _, err := NewMem0Adaptive(Mem0Config{BaseURL: "ftp://nope"}); err == nil {
		t.Fatal("non-http base_url must be rejected")
	}
	m, err := NewMem0Adaptive(Mem0Config{BaseURL: "https://api.mem0.ai", APIKey: "k"})
	if err != nil || m.Style() != "platform" {
		t.Fatalf("platform style not inferred: %v %v", err, m)
	}
}

func TestMem0ProviderFailureIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	m, _ := NewMem0Adaptive(Mem0Config{BaseURL: srv.URL})
	_, err := m.Recall(context.Background(), scope, "anything at all", 3)
	if err == nil || !strings.Contains(err.Error(), "provider request failed") {
		t.Fatalf("expected typed provider failure, got %v", err)
	}
}
