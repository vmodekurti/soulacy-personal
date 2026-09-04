package runtime

import (
	"context"
	"testing"
	"time"
)

func TestToolTimeoutOverride_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := toolTimeoutOverride(ctx); ok {
		t.Fatal("no override should be present on a bare context")
	}
	ctx2 := WithToolTimeout(ctx, 5*time.Minute)
	d, ok := toolTimeoutOverride(ctx2)
	if !ok || d != 5*time.Minute {
		t.Fatalf("override not carried: got %v ok=%v", d, ok)
	}
	// Non-positive durations are a no-op (keep the global).
	if c := WithToolTimeout(ctx, 0); c != ctx {
		t.Errorf("zero duration should be a no-op")
	}
}

func TestEffectiveToolTimeout_Precedence(t *testing.T) {
	e := &Engine{toolTimeout: 120 * time.Second}

	// No override → global.
	if got := e.effectiveToolTimeout(context.Background()); got != 120*time.Second {
		t.Errorf("want global 120s, got %v", got)
	}
	// Node override → wins over global.
	ctx := WithToolTimeout(context.Background(), 10*time.Minute)
	if got := e.effectiveToolTimeout(ctx); got != 10*time.Minute {
		t.Errorf("node override should win; want 10m, got %v", got)
	}
}
