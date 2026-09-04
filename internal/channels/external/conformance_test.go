package external

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRunConformance_PassesForConformingSidecar(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "happy")
	if err := RunConformance(context.Background(), os.Args[0], "-test.run=TestHelperSidecar"); err != nil {
		t.Fatalf("conforming sidecar failed: %v", err)
	}
}

func TestRunConformance_FailsWithoutHello(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "nohello")
	err := RunConformance(context.Background(), os.Args[0], "-test.run=TestHelperSidecar")
	if err == nil || !strings.Contains(err.Error(), "hello") {
		t.Fatalf("err = %v, want hello-related failure", err)
	}
}

func TestRunConformance_FailsOnBadVersion(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "badversion")
	err := RunConformance(context.Background(), os.Args[0], "-test.run=TestHelperSidecar")
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("err = %v, want protocol-related failure", err)
	}
}
