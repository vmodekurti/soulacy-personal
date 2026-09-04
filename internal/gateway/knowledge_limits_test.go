package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestRequestBodyLimitTracksKnowledgeDocumentLimit(t *testing.T) {
	s := &Server{cfg: &config.Config{Knowledge: config.KnowledgeConfig{MaxDocumentBytes: 1234}}}
	if got, want := s.requestBodyLimit(), 1234+(1<<20); got != want {
		t.Fatalf("requestBodyLimit = %d, want %d", got, want)
	}
}

func TestKnowledgeMaxDocumentBytesFallsBackToProductionDefault(t *testing.T) {
	s := &Server{}
	if got := s.knowledgeMaxDocumentBytes(); got != 50<<20 {
		t.Fatalf("knowledgeMaxDocumentBytes = %d, want %d", got, int64(50<<20))
	}
}
