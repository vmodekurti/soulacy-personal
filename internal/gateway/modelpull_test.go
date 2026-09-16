package gateway

import (
	"net/http"
	"testing"
	"time"
)

func TestModelPullRejectsAnEmptyModelName(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama/models/pull", "secret", `{"model":"  "}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body = %v", status, body)
	}
	if _, ok := body["error"]; !ok {
		t.Error("a rejection should say why")
	}
}

// Pulling into a hosted provider is meaningless — its catalog is not ours to
// change — and the message should say so rather than failing obscurely.
func TestModelPullRefusesNonLocalProvidersWithAReason(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic/models/pull", "secret", `{"model":"claude"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body = %v", status, body)
	}
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Fatal("expected an explanation")
	}
}

func TestModelPullStatusIsNotFoundForAnUnknownJob(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers/ollama/models/pull/nope", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// Downloading a model writes to disk and costs bandwidth, so it is a write.
func TestModelPullRequiresProviderWrite(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama/models/pull", "wrong-key", `{"model":"qwen3:4b"}`)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("status = %d, want 401/403", status)
	}
}

func TestSuggestedModelsAreOfferedWithFitAgainstHostMemory(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers/ollama/models/suggested", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body = %v", status, body)
	}
	models, _ := body["models"].([]any)
	if len(models) == 0 {
		t.Fatal("there should be something to suggest on a machine with no models")
	}
	var defaults int
	for _, raw := range models {
		m, _ := raw.(map[string]any)
		if m["name"] == "" || m["name"] == nil {
			t.Error("every suggestion needs a name to pull")
		}
		if _, ok := m["size_gb"]; !ok {
			t.Error("a user deserves to know the download size before starting it")
		}
		if summary, _ := m["summary"].(string); summary == "" {
			t.Error("a suggestion without a reason is not a suggestion")
		}
		if d, _ := m["default"].(bool); d {
			defaults++
		}
	}
	// Exactly one recommended choice, or none if the machine fits nothing.
	if defaults > 1 {
		t.Errorf("want at most one default, got %d", defaults)
	}
}

// The registry hands out copies, and keeps finished jobs briefly so a poll
// that lands just after completion sees the outcome instead of a 404.
func TestPullRegistryKeepsFinishedJobsAndHandsOutCopies(t *testing.T) {
	var r pullRegistry
	j := &pullJob{ID: "a", Provider: "ollama", Model: "qwen3:4b", Status: "starting"}
	r.put(j)

	got, ok := r.get("a")
	if !ok {
		t.Fatal("job should be retrievable")
	}
	got.Status = "mutated by caller"
	if again, _ := r.get("a"); again.Status != "starting" {
		t.Error("get must return a copy; a caller mutated the live job")
	}

	r.update("a", func(j *pullJob) { j.Done = true; j.Finished = time.Now().UTC() })
	if fresh, _ := r.get("a"); !fresh.Done {
		t.Error("update should have marked it done")
	}
	// Recently finished stays visible.
	r.put(&pullJob{ID: "b"})
	if _, ok := r.get("a"); !ok {
		t.Error("a just-finished job must survive the sweep, or the dashboard shows an error on success")
	}
}

// A second click, or a second tab, must join the running download rather than
// starting a competing one.
func TestPullRegistryFindsAnActiveJobForTheSameModel(t *testing.T) {
	var r pullRegistry
	r.put(&pullJob{ID: "a", Provider: "ollama", Model: "qwen3:4b"})
	if id, ok := r.active("ollama", "qwen3:4b"); !ok || id != "a" {
		t.Fatalf("active = %q,%v; want a,true", id, ok)
	}
	if _, ok := r.active("ollama", "something-else"); ok {
		t.Error("a different model is a different download")
	}
	r.update("a", func(j *pullJob) { j.Done = true })
	if _, ok := r.active("ollama", "qwen3:4b"); ok {
		t.Error("a finished job is not active")
	}
}

// Every curated name is pulled verbatim, so a typo here is a dead end for a
// first-time user. These were checked against the live registry when added.
func TestCuratedModelsAreWellFormed(t *testing.T) {
	if len(curatedModels) == 0 {
		t.Fatal("the empty state needs something to offer")
	}
	seen := map[string]bool{}
	var embeddings int
	for _, m := range curatedModels {
		if seen[m.Name] {
			t.Errorf("duplicate suggestion %q", m.Name)
		}
		seen[m.Name] = true
		if m.SizeGB <= 0 {
			t.Errorf("%s has no download size", m.Name)
		}
		if m.MinRAMGB <= 0 {
			t.Errorf("%s has no memory floor, so it would be offered to any machine", m.Name)
		}
		if m.Summary == "" {
			t.Errorf("%s has no explanation", m.Name)
		}
		if m.Embedding {
			embeddings++
		}
	}
	// Knowledge search needs an embedding model; without one in the list a
	// user can set up chat and then hit a second dead end in Knowledge.
	if embeddings == 0 {
		t.Error("the list should include an embedding model")
	}
}
