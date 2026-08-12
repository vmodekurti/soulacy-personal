package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/memory"
)

type semanticTestEmbedder struct{}

func (semanticTestEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(strings.ToLower(text), "weather") {
		return []float32{1, 0, 0}, nil
	}
	return []float32{0, 1, 0}, nil
}

func TestAgentMemoryVectorAdapterUsesPersistentSQLiteVec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	archive, err := memory.NewSQLiteArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	vectorStore, err := memory.NewVectorStore(archive.DB(), semanticTestEmbedder{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	store := agentmemory.NewCompositeStore(t.TempDir(), &agentMemoryVectorAdapter{store: vectorStore})
	if err := store.Write(agentmemory.Record{ID: "weather", AgentID: "a", Type: agentmemory.MemoryTypeSemantic, Content: "weather forecast guidance"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(agentmemory.Record{ID: "finance", AgentID: "a", Type: agentmemory.MemoryTypeSemantic, Content: "portfolio accounting guidance"}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Retrieve(agentmemory.RetrieveQuery{AgentID: "a", TaskInput: "weather tomorrow", MaxSemantic: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SemanticChunks) != 1 || result.SemanticChunks[0].ID != "weather" {
		t.Fatalf("semantic result = %+v", result.SemanticChunks)
	}

	// Reopen the archive to prove the backend is durable, not process memory.
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := memory.NewSQLiteArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedVectors, err := memory.NewVectorStore(reopened.DB(), semanticTestEmbedder{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	reopenedStore := agentmemory.NewCompositeStore(t.TempDir(), &agentMemoryVectorAdapter{store: reopenedVectors})
	result, err = reopenedStore.Retrieve(agentmemory.RetrieveQuery{AgentID: "a", TaskInput: "weather tomorrow", MaxSemantic: 1})
	if err != nil || len(result.SemanticChunks) != 1 || result.SemanticChunks[0].ID != "weather" {
		t.Fatalf("reopened semantic result = %+v err=%v", result.SemanticChunks, err)
	}
}
