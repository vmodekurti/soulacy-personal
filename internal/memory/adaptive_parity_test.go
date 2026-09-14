package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── contradiction → retract ─────────────────────────────────────────────────

func TestNegationRetractsOldFactWithoutStoringTheNegation(t *testing.T) {
	store := newFactStore(t)
	emb := &wordEmbedder{vocab: []string{"has", "dog", "named", "rex", "longer"}}
	sc := &scriptedCompleter{replies: []string{
		`{"facts":[{"fact":"User has a dog named Rex","category":"entity","confidence":0.9}],"relations":[{"subject":"User","predicate":"has dog","object":"Rex"}]}`,
		`{"facts":[{"fact":"User no longer has a dog","category":"entity","confidence":0.9}],"relations":[]}`,
		`{"decision":"retract"}`,
	}}
	eng := NewLocalAdaptive(store, emb, sc.fn, LocalOptions{SimilarityThreshold: 0.3})
	ctx := context.Background()
	first, err := eng.Remember(ctx, scope, Turn{SessionID: "s1", User: "I have a dog named Rex"})
	if err != nil || len(first.Added) != 1 || first.RelationsAdded != 1 {
		t.Fatalf("first turn: %v %+v", err, first)
	}
	out, err := eng.Remember(ctx, scope, Turn{SessionID: "s2", User: "Sad news, I no longer have a dog"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Retracted) != 1 || len(out.Added) != 0 {
		t.Fatalf("expected a retraction and no new fact, got %+v", out)
	}
	active, _ := store.List(ctx, scope, FactStatusActive, 0)
	if len(active) != 0 {
		t.Fatalf("retracted fact still active: %+v", active)
	}
	retracted, _ := store.List(ctx, scope, FactStatusRetracted, 0)
	if len(retracted) != 1 {
		t.Fatalf("retracted fact should be kept for audit, got %d", len(retracted))
	}
	hist, err := eng.History(ctx, scope, retracted[0].ID)
	if err != nil || len(hist) != 2 || hist[0].Event != "retracted" || hist[1].Event != "created" {
		t.Fatalf("history should show created then retracted: %v %+v", err, hist)
	}
}

func TestRuleFallbackDetectsNegation(t *testing.T) {
	old := Fact{Content: "User drinks coffee every morning", Category: FactPreference}
	cand := Fact{Content: "User does not drink coffee anymore", Category: FactPreference}
	res := ResolveCandidate(context.Background(), nil, cand, []ScoredFact{{Fact: old, Score: 0.9}}, 0.85)
	if res.Decision != DecisionRetract {
		t.Fatalf("rule fallback should retract on negation, got %s", res.Decision)
	}
}

// ── graph relations ─────────────────────────────────────────────────────────

func TestRelationsSupersedeSameSubjectPredicateAndAppearInPrompt(t *testing.T) {
	store := newFactStore(t)
	eng := NewLocalAdaptive(store, nil, nil, LocalOptions{})
	ctx := context.Background()
	if _, created, err := store.UpsertRelation(ctx, scope, Relation{Subject: "User", Predicate: "works at", Object: "Acme"}); err != nil || !created {
		t.Fatalf("first relation: %v %v", err, created)
	}
	if _, created, _ := store.UpsertRelation(ctx, scope, Relation{Subject: "user", Predicate: "works at", Object: "acme"}); created {
		t.Fatal("identical triple must be a no-op")
	}
	if _, created, _ := store.UpsertRelation(ctx, scope, Relation{Subject: "User", Predicate: "works at", Object: "Globex"}); !created {
		t.Fatal("new object for same subject+predicate must be stored")
	}
	active, _ := store.Relations(ctx, scope, FactStatusActive, 0)
	if len(active) != 1 || active[0].Object != "Globex" {
		t.Fatalf("old relation should be superseded: %+v", active)
	}
	all, _ := store.Relations(ctx, scope, "", 0)
	if len(all) != 2 {
		t.Fatalf("superseded relation should be kept, got %d", len(all))
	}
	rels, err := eng.Relations(ctx, scope, "where does the user work", 3)
	if err != nil || len(rels) != 1 {
		t.Fatalf("relations recall: %v %+v", err, rels)
	}
	block := FormatPromptBlock(nil, 50, rels...)
	if !strings.Contains(block, "- User works at Globex") {
		t.Fatalf("relation missing from block: %q", block)
	}
	other, _ := eng.Relations(ctx, FactScope{Owner: "bob", AgentID: "helper"}, "", 3)
	if len(other) != 0 {
		t.Fatal("relations leaked across owners")
	}
}

// ── session-scoped ("temporary") facts ──────────────────────────────────────

func TestTemporaryFactsOnlyRecalledInTheirSession(t *testing.T) {
	store := newFactStore(t)
	sc := &scriptedCompleter{replies: []string{
		`{"facts":[{"fact":"User wants answers in French for this conversation","category":"preference","confidence":0.9,"temporary":true},{"fact":"User lives in Lyon","category":"identity","confidence":0.9}]}`,
	}}
	eng := NewLocalAdaptive(store, nil, sc.fn, LocalOptions{})
	ctx := context.Background()
	if _, err := eng.Remember(ctx, scope, Turn{SessionID: "s1", User: "For this chat answer in French. I live in Lyon."}); err != nil {
		t.Fatal(err)
	}
	same := scope
	same.SessionID = "s1"
	hits, _ := eng.Recall(ctx, same, "", 10)
	if len(hits) != 2 {
		t.Fatalf("same session should see both facts, got %d", len(hits))
	}
	other := scope
	other.SessionID = "s2"
	hits, _ = eng.Recall(ctx, other, "", 10)
	if len(hits) != 1 || hits[0].Content != "User lives in Lyon" {
		t.Fatalf("other session should only see the durable fact: %+v", hits)
	}
	all, _ := eng.List(ctx, scope, FactStatusActive, 0)
	if len(all) != 2 {
		t.Fatal("management list must still show session-scoped facts")
	}
}

// ── expiration ──────────────────────────────────────────────────────────────

func TestExpiredFactsAreHiddenFromRecallButListed(t *testing.T) {
	store := newFactStore(t)
	eng := NewLocalAdaptive(store, nil, nil, LocalOptions{})
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	if _, err := eng.Add(ctx, scope, FactInput{Category: FactConstraint, Content: "User is on vacation until yesterday", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	f, err := eng.Add(ctx, scope, FactInput{Category: FactConstraint, Content: "User is travelling this week", ExpiresAt: &future})
	if err != nil {
		t.Fatal(err)
	}
	hits, _ := eng.Recall(ctx, scope, "", 10)
	if len(hits) != 1 || hits[0].ID != f.ID {
		t.Fatalf("expired fact must not be recalled: %+v", hits)
	}
	all, _ := eng.List(ctx, scope, FactStatusActive, 0)
	if len(all) != 2 || all[0].ExpiresAt == nil {
		t.Fatalf("list should include expiry: %+v", all)
	}
	if _, err := eng.Update(ctx, scope, f.ID, FactInput{Category: FactConstraint, Content: "User is travelling this week", ExpiresAt: nil}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, scope, f.ID)
	if got.ExpiresAt != nil {
		t.Fatal("update should clear expiry when none is given")
	}
}

// ── per-fact history ────────────────────────────────────────────────────────

func TestHistoryRecordsEveryChangeAndSurvivesDeletion(t *testing.T) {
	store := newFactStore(t)
	eng := NewLocalAdaptive(store, nil, nil, LocalOptions{})
	ctx := context.Background()
	f, _ := eng.Add(ctx, scope, FactInput{Category: FactPreference, Content: "User likes tea"})
	if _, err := eng.Update(ctx, scope, f.ID, FactInput{Category: FactPreference, Content: "User likes green tea"}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Delete(ctx, scope, f.ID); err != nil {
		t.Fatal(err)
	}
	hist, err := eng.History(ctx, scope, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	for _, e := range hist {
		events = append(events, e.Event+":"+e.Content)
	}
	want := "deleted:User likes green tea|updated:User likes tea|created:User likes tea"
	if strings.Join(events, "|") != want {
		t.Fatalf("history mismatch:\n got %s\nwant %s", strings.Join(events, "|"), want)
	}
	if hist[0].Actor != "user" {
		t.Fatalf("manual edits should be attributed to the user, got %q", hist[0].Actor)
	}
	if _, err := eng.History(ctx, FactScope{Owner: "bob", AgentID: "helper"}, f.ID); err == nil {
		t.Fatal("history must not be readable across owners")
	}
}

// ── custom instructions and categories ─────────────────────────────────────

func TestCustomInstructionsAndCategoriesReachExtraction(t *testing.T) {
	store := newFactStore(t)
	sc := &scriptedCompleter{replies: []string{
		`{"facts":[{"fact":"User is a staff engineer on the payments team","category":"role","confidence":0.9},{"fact":"User likes puns","category":"humor","confidence":0.9}]}`,
	}}
	eng := NewLocalAdaptive(store, nil, sc.fn, LocalOptions{Instructions: "Also capture the user's job title and team.", CustomCategories: []string{"Role", "role", "bad category!"}})
	ctx := context.Background()
	out, err := eng.Remember(ctx, scope, Turn{SessionID: "s1", User: "I'm a staff engineer on the payments team and I like puns"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Added) != 1 {
		t.Fatalf("only the allowed custom category should be stored: %+v", out)
	}
	if !strings.Contains(sc.calls[0], "job title and team") || !strings.Contains(sc.calls[0], "entity|role") {
		t.Fatalf("instructions/categories missing from prompt:\n%s", sc.calls[0])
	}
	if got := eng.Categories(); strings.Join(got, ",") != "preference,identity,constraint,entity,role" {
		t.Fatalf("categories: %v", got)
	}
	if _, err := eng.Add(ctx, scope, FactInput{Category: "humor", Content: "x"}); err == nil {
		t.Fatal("unknown category must be rejected on manual add")
	}
	if _, err := eng.Add(ctx, scope, FactInput{Category: "role", Content: "User leads the payments team"}); err != nil {
		t.Fatalf("custom category must be accepted: %v", err)
	}
}

func TestDefaultExtractionPromptStillUnder200TokensWithRelations(t *testing.T) {
	long := strings.Repeat("I prefer concise answers and I live in Denver with my dog Rex. ", 20)
	system, user := ExtractionPrompt(Turn{User: long, Assistant: strings.Repeat("Sure, noted. ", 40)}, ExtractOptions{})
	if got := EstimateTokens(system + "\n" + user); got >= 200 {
		t.Fatalf("default extraction prompt estimated at %d tokens, want < 200", got)
	}
}

// ── mem0 parity: relations, history, expiry, custom instructions ────────────

func TestMem0AdapterRelationsHistoryAndExpiry(t *testing.T) {
	var addBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/memories/":
			_ = json.NewDecoder(r.Body).Decode(&addBody)
			_, _ = w.Write([]byte(`{"results":[{"id":"m1","memory":"User works at Acme","event":"ADD"},{"id":"m0","event":"DELETE"}],"relations":[{"source":"user","relationship":"works_at","target":"acme"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/memories/search/":
			_, _ = w.Write([]byte(`{"results":[{"id":"m1","memory":"User works at Acme","score":0.8,"expiration_date":"2020-01-01"}],"relations":[{"source":"user","relationship":"works_at","target":"acme"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/memories/m1/history/":
			_, _ = w.Write([]byte(`[{"id":"h1","memory_id":"m1","old_memory":null,"new_memory":"User works at Initech","event":"ADD","created_at":"2026-09-01T10:00:00Z"},{"id":"h2","memory_id":"m1","old_memory":"User works at Initech","new_memory":"User works at Acme","event":"UPDATE","updated_at":"2026-09-02T10:00:00Z"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	m, err := NewMem0Adaptive(Mem0Config{BaseURL: srv.URL, APIKey: "k", APIStyle: "platform", EnableGraph: true, Instructions: "capture job titles", CustomCategories: []string{"role"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, err := m.Remember(ctx, scope, Turn{User: "I work at Acme now, not Initech"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Retracted) != 1 || out.RelationsAdded != 1 || len(out.Added) != 1 {
		t.Fatalf("mem0 events not mapped: %+v", out)
	}
	if addBody["custom_instructions"] != "capture job titles" || addBody["enable_graph"] != true {
		t.Fatalf("instructions/graph not forwarded: %+v", addBody)
	}
	if cats, ok := addBody["custom_categories"].(map[string]any); !ok || cats["role"] == nil {
		t.Fatalf("custom categories not forwarded: %+v", addBody["custom_categories"])
	}
	rels, err := m.Relations(ctx, scope, "work", 3)
	if err != nil || len(rels) != 1 || rels[0].Sentence() != "user works at acme" {
		t.Fatalf("relations: %v %+v", err, rels)
	}
	hits, _ := m.Recall(ctx, scope, "work", 3)
	if len(hits) != 0 {
		t.Fatalf("expired provider memory must be hidden from recall: %+v", hits)
	}
	hist, err := m.History(ctx, scope, "m1")
	if err != nil || len(hist) != 2 || hist[0].Event != "updated" || hist[0].Content != "User works at Initech" || hist[1].Event != "created" {
		t.Fatalf("history mapping: %v %+v", err, hist)
	}
	if got := m.Categories(); strings.Join(got, ",") != "preference,identity,constraint,entity,role" {
		t.Fatalf("categories: %v", got)
	}
}
