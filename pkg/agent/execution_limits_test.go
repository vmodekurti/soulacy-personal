package agent

import (
	"testing"
	"time"
)

func TestExecutionTimeoutZeroIsUnlimitedAndOmittedInherits(t *testing.T) {
	for _, raw := range []string{"0", "0s"} {
		if got := ParseExecutionTimeout(raw, time.Minute); got != time.Duration(1<<63-1) {
			t.Fatalf("%q: %v", raw, got)
		}
	}
	if got := ParseExecutionTimeout("", time.Hour); got != time.Hour {
		t.Fatal(got)
	}
	if got := ParseExecutionTimeout("30s", time.Hour); got != 30*time.Second {
		t.Fatal(got)
	}
}

func TestResolveTurnsExplicitUnlimitedStillHonorsCeiling(t *testing.T) {
	for _, tc := range []struct{ request, inherited, ceiling, want int }{{0, 256, 0, 256}, {-1, 256, 0, 0}, {0, 0, 0, 0}, {-1, 256, 10, 10}, {1000, 256, 500, 500}, {2, 256, 500, 2}} {
		if got := ResolveTurns(tc.request, tc.inherited, tc.ceiling); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
}
