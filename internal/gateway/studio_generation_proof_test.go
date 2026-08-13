package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/internal/studio"
)

func TestGenerationProofBindsOwnerAndExactBaselineOnce(t *testing.T) {
	s := &Server{generationProofs: make(map[string]generationProofRecord)}
	baseline := studio.Draft{Name: "Original", Intent: "build an assistant"}
	s.issueGenerationProof("alice", &baseline)
	forged := baseline
	forged.Intent = "fabricated baseline"
	if s.verifyGenerationProof("alice", forged) {
		t.Fatal("forged baseline verified")
	}
	if s.verifyGenerationProof("bob", baseline) {
		t.Fatal("cross-owner baseline verified")
	}
	if !s.verifyGenerationProof("alice", baseline) {
		t.Fatal("issued baseline did not verify")
	}
	if s.verifyGenerationProof("alice", baseline) {
		t.Fatal("proof was reusable")
	}
}
