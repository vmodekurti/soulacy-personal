package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type semanticLessonEmbedder struct{ calls int }

func (e *semanticLessonEmbedder) Identity() string { return "semantic-test-v1" }

func (e *semanticLessonEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls++
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "pagination") || strings.Contains(text, "page token"):
		return []float32{1, 0, 0}, nil
	case strings.Contains(text, "weather"):
		return []float32{0, 1, 0}, nil
	default:
		return []float32{0, 0, 1}, nil
	}
}

func TestLessonFromProposal(t *testing.T) {
	// A shape drift with a rationale becomes a tool-scoped lesson.
	p := RepairProposal{
		NodeID: "fmt", Field: "input", Class: RepairShapeDrift,
		Rationale:    "The API response has no \"results\"; the list is under \"items\".",
		ObservedKeys: []string{"items", "meta"},
	}
	l, ok := LessonFromProposal(p, "web_search", "news digest")
	if !ok {
		t.Fatal("expected a lesson")
	}
	if !strings.Contains(l.Guidance, "web_search") || !strings.Contains(l.Guidance, "items") {
		t.Fatalf("guidance missing tool/shape: %q", l.Guidance)
	}
	if l.Count != 1 || l.ID == "" {
		t.Fatalf("bad lesson: %+v", l)
	}

	// A tool_failure teaches nothing durable.
	if _, ok := LessonFromProposal(RepairProposal{Class: RepairToolFailure, Rationale: "401"}, "web_search", ""); ok {
		t.Error("tool_failure should not produce a lesson")
	}
}

func TestLessonStore_AddMergeAndRelevant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lessons.json")
	st := NewLessonStore(path)

	l, _ := LessonFromProposal(RepairProposal{
		Class: RepairShapeDrift, Rationale: "list is under items", ObservedKeys: []string{"items"},
	}, "web_search", "")
	if err := st.Add(l); err != nil {
		t.Fatal(err)
	}
	// Same lesson again → merge (Count++), not a duplicate.
	if err := st.Add(l); err != nil {
		t.Fatal(err)
	}
	all := st.All()
	if len(all) != 1 || all[0].Count != 2 {
		t.Fatalf("expected 1 merged lesson count=2, got %+v", all)
	}

	// A different tool's lesson.
	l2, _ := LessonFromProposal(RepairProposal{
		Class: RepairShapeDrift, Rationale: "response is a JSON string; parse it",
	}, "http_get", "")
	_ = st.Add(l2)

	// Relevant to web_search only → the http_get lesson is excluded.
	rel := st.Relevant([]string{"web_search"}, 8)
	if len(rel) != 1 || rel[0].Tool != "web_search" {
		t.Fatalf("relevance filter failed: %+v", rel)
	}
	// Relevant to both → both.
	if got := st.Relevant([]string{"web_search", "http_get"}, 8); len(got) != 2 {
		t.Fatalf("expected 2 relevant, got %d", len(got))
	}
	// Ranked by Count: web_search (2) before http_get (1).
	if got := st.Relevant([]string{"web_search", "http_get"}, 8); got[0].Tool != "web_search" {
		t.Fatalf("expected count ranking, got %+v", got)
	}
}

// A general (tool-less) lesson surfaces regardless of tools in use.
func TestLessonStore_GeneralLessonAlwaysRelevant(t *testing.T) {
	st := NewLessonStore(filepath.Join(t.TempDir(), "l.json"))
	l, _ := LessonFromProposal(RepairProposal{Class: RepairShapeDrift, Rationale: "always wrap with toJson"}, "", "")
	_ = st.Add(l)
	if got := st.Relevant([]string{"anything"}, 8); len(got) != 1 {
		t.Fatalf("general lesson should always be relevant, got %+v", got)
	}
}

func TestAppendGenSample_DedupCapAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.json")
	valid := `{"name":"E","trigger":{"type":"channel"},"flow":{"nodes":[{"id":"a","kind":"agent","agent":"h","input":"{{ .trigger.text }}","output":"r"}],"edges":[{"from":"a","to":"end"}],"entry":"a"}}`

	if err := AppendGenSample(path, GenSample{Name: "repair:X:a", Raw: valid, Recoverable: true}, 200); err != nil {
		t.Fatal(err)
	}
	// Same name → replace, not duplicate.
	if err := AppendGenSample(path, GenSample{Name: "repair:X:a", Raw: valid, Recoverable: true}, 200); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	samples, err := LoadGenSamples(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 deduped sample, got %d", len(samples))
	}
	// The persisted fixed draft round-trips through the corpus scorer as valid.
	rep := RunGenerationCorpus(samples)
	if rep.Recovered != 1 || len(rep.Failures) != 0 {
		t.Fatalf("persisted repair case should score valid: %+v", rep)
	}
}

func TestLessonsPromptBlock(t *testing.T) {
	if LessonsPromptBlock(nil) != "" {
		t.Error("empty lessons should yield empty block")
	}
	block := LessonsPromptBlock([]Lesson{
		{Guidance: "When using `web_search`: list is under items", ObservedKeys: []string{"items", "meta"}},
	})
	if !strings.Contains(block, "LESSONS FROM PAST RUNS") || !strings.Contains(block, "web_search") {
		t.Fatalf("block missing content: %q", block)
	}
	if !strings.Contains(block, "observed keys: items, meta") {
		t.Fatalf("block missing observed keys: %q", block)
	}
}

func TestLessonStoreSemanticRetrievalAcrossDifferentTools(t *testing.T) {
	embedder := &semanticLessonEmbedder{}
	store := NewSemanticLessonStore(filepath.Join(t.TempDir(), "lessons.db"), embedder)
	github := Lesson{Tool: "github_api", Class: "shape_drift", Guidance: "Follow pagination using the returned page token.", Count: 1}
	weather := Lesson{Tool: "weather_api", Class: "shape_drift", Guidance: "Weather temperatures are returned in Celsius.", Count: 1}
	if err := store.Add(github); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(weather); err != nil {
		t.Fatal(err)
	}
	if embedder.calls < 2 {
		t.Fatalf("lessons were not embedded on save; calls=%d", embedder.calls)
	}
	got := store.Semantic(context.Background(), "Use gitlab_api and handle every pagination page token", 5, 0.8)
	if len(got) != 1 || got[0].Tool != "github_api" {
		t.Fatalf("semantic cross-tool result = %+v", got)
	}
}

func TestLessonStoreMigratesLegacyJSONToSQLiteVec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "studio-lessons.db")
	lesson := Lesson{ID: "legacy", Tool: "github_api", Guidance: "Handle pagination with page tokens.", Count: 2, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	raw, _ := json.Marshal([]Lesson{lesson})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewLessonStore(path)
	all := store.All()
	if len(all) != 1 || all[0].ID != "legacy" || all[0].Count != 2 {
		t.Fatalf("legacy import = %+v", all)
	}
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(header), "SQLite format 3") {
		t.Fatalf("store is not SQLite after migration: %q", header[:min(len(header), 16)])
	}
	if _, err := os.Stat(path + ".legacy.json"); err != nil {
		t.Fatalf("legacy backup missing: %v", err)
	}
}

type identityEmbedder struct {
	identity    string
	queryVector []float32
}

func (e identityEmbedder) Identity() string { return e.identity }
func (e identityEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, "needle") {
		return append([]float32(nil), e.queryVector...), nil
	}
	return append([]float32(nil), e.queryVector...), nil
}

func TestLessonStoreReindexesWhenEmbeddingIdentityChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lessons.db")
	first := NewSemanticLessonStore(path, identityEmbedder{identity: "model-a", queryVector: []float32{1, 0}})
	if err := first.Add(Lesson{ID: "one", Guidance: "needle pagination guidance"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := NewSemanticLessonStore(path, identityEmbedder{identity: "model-b", queryVector: []float32{0, 1}})
	defer second.Close()
	if got := second.Semantic(context.Background(), "needle", 5, 0.9); len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("reindexed results=%+v", got)
	}
}

// BuildPrompt injects the lessons block when the catalog carries lessons.
func TestBuildPrompt_InjectsLessons(t *testing.T) {
	cat := Catalog{
		Tools:   []string{"web_search"},
		Lessons: []Lesson{{Guidance: "When using `web_search`: results are under items"}},
	}
	prompt := BuildPrompt("build a news digest", cat, nil)
	if !strings.Contains(prompt, "LESSONS FROM PAST RUNS") || !strings.Contains(prompt, "results are under items") {
		t.Fatal("BuildPrompt did not inject lessons")
	}
}
