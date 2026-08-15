// workspace_test.go — the tenant pre-filter contract for the Qdrant backend.
//
// Qdrant is a network service, so these tests assert the *request* the store
// builds rather than search results. That is the property that matters: the
// workspace has to reach Qdrant's pre-filter, because a filter applied after
// the ANN search would already have spent the topK budget on another tenant's
// points.
package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

type captured struct{ body map[string]any }

// newCapturingStore stands up a fake Qdrant that records the search request.
func newCapturingStore(t *testing.T, seen *captured) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/search") {
			_ = json.NewDecoder(r.Body).Decode(&seen.body)
			_, _ = w.Write([]byte(`{"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	t.Cleanup(server.Close)
	store, err := New(context.Background(), Config{
		BaseURL: server.URL, Collection: "memories", Dims: 4, Embedder: stubEmbedder{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

type stubEmbedder struct{}

func (stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0, 0}, nil
}

func mustList(t *testing.T, v any) []any {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("expected a list, got %T", v)
	}
	return list
}

// A tenant's search must carry workspace_id as a `must` clause, so Qdrant
// narrows the candidate set before the ANN search rather than after it.
func TestSearchSendsTheWorkspaceAsAPreFilter(t *testing.T) {
	var seen captured
	store := newCapturingStore(t, &seen)

	if _, err := store.SearchInWorkspace(context.Background(), "ws_a", "assistant", "anything", 5); err != nil {
		t.Fatal(err)
	}
	filter, ok := seen.body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("no filter was sent: %+v", seen.body)
	}
	must := mustList(t, filter["must"])
	var sawWorkspace, sawAgent bool
	for _, clause := range must {
		entry, _ := clause.(map[string]any)
		switch entry["key"] {
		case "workspace_id":
			match, _ := entry["match"].(map[string]any)
			if match["value"] == "ws_a" {
				sawWorkspace = true
			}
		case "agent_id":
			sawAgent = true
		}
	}
	if !sawWorkspace {
		t.Errorf("the workspace was not sent as a pre-filter: %+v", must)
	}
	if !sawAgent {
		t.Errorf("the agent filter was lost: %+v", must)
	}
}

// Points written before tenancy carry no workspace_id and belong to the
// personal workspace. A personal search must match those as well as tagged
// ones — otherwise an existing single-user installation's memories silently
// stop being recallable. No other tenant can reach either branch.
func TestPersonalSearchAlsoMatchesUntaggedLegacyPoints(t *testing.T) {
	var seen captured
	store := newCapturingStore(t, &seen)

	if _, err := store.SearchInWorkspace(context.Background(), wsroot.PersonalWorkspaceID, "", "anything", 5); err != nil {
		t.Fatal(err)
	}
	filter, ok := seen.body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("no filter was sent: %+v", seen.body)
	}
	should := mustList(t, filter["should"])
	var sawMatch, sawEmpty bool
	for _, clause := range should {
		entry, _ := clause.(map[string]any)
		if entry["key"] == "workspace_id" {
			sawMatch = true
		}
		if _, isEmpty := entry["is_empty"]; isEmpty {
			sawEmpty = true
		}
	}
	if !sawMatch || !sawEmpty {
		t.Fatalf("a personal search does not cover untagged legacy points: %+v", should)
	}
}

// The frozen Backend.Search resolves to the personal workspace, which is what
// a single-tenant caller is.
func TestFrozenSearchResolvesToPersonal(t *testing.T) {
	var seen captured
	store := newCapturingStore(t, &seen)
	if _, err := store.Search(context.Background(), "", "anything", 5); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen.body["filter"].(map[string]any)["should"]; !ok {
		t.Fatalf("the frozen Search did not resolve to the personal workspace: %+v", seen.body)
	}
}
