package qdrant

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/memory"
)

// TestLiveQdrantEnforcesWorkspacePrefilter closes the gap a request-capture
// fake cannot: it proves a real Qdrant server executes the workspace filter.
// The release workflow supplies SOULACY_TEST_QDRANT_URL and fails if this test
// is skipped.
func TestLiveQdrantEnforcesWorkspacePrefilter(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("SOULACY_TEST_QDRANT_URL"))
	if baseURL == "" {
		t.Skip("SOULACY_TEST_QDRANT_URL is required for the live Qdrant release gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collection := "soulacy_release_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	store, err := New(ctx, Config{BaseURL: baseURL, Collection: collection, Dims: 4, Embedder: stubEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for _, entry := range []memory.Entry{
		{ID: uuid.NewString(), WorkspaceID: "ws_a", AgentID: "assistant", SessionID: "s", Content: "same-vector-a", CreatedAt: now},
		{ID: uuid.NewString(), WorkspaceID: "ws_b", AgentID: "assistant", SessionID: "s", Content: "same-vector-b", CreatedAt: now},
	} {
		if err := store.Write(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	for workspaceID, want := range map[string]string{"ws_a": "same-vector-a", "ws_b": "same-vector-b"} {
		var results []struct{ Content string }
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			hits, err := store.SearchInWorkspace(ctx, workspaceID, "assistant", "query", 10)
			if err != nil {
				t.Fatal(err)
			}
			results = results[:0]
			for _, hit := range hits {
				results = append(results, struct{ Content string }{hit.Entry.Content})
			}
			if len(results) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(results) != 1 || results[0].Content != want {
			t.Fatalf("%s search returned %+v, want only %q", workspaceID, results, want)
		}
	}
	t.Log("SOULACY_QDRANT_LIVE_GATE=passed")
}
