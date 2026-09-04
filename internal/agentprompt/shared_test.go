package agentprompt

import (
	"strings"
	"testing"
)

func TestEnsureSharedPrependsContractAndRole(t *testing.T) {
	got := EnsureShared("You are a research assistant.")
	if !strings.HasPrefix(got, marker) {
		t.Fatalf("prompt should start with shared contract:\n%s", got)
	}
	if !strings.Contains(got, "## Agent Role\n\nYou are a research assistant.") {
		t.Fatalf("prompt should include role section:\n%s", got)
	}
}

func TestEnsureSharedIsIdempotent(t *testing.T) {
	once := EnsureShared("You are a research assistant.")
	twice := EnsureShared(once)
	if once != twice {
		t.Fatalf("EnsureShared should be idempotent")
	}
	if n := strings.Count(twice, marker); n != 1 {
		t.Fatalf("shared contract marker count = %d, want 1", n)
	}
}
