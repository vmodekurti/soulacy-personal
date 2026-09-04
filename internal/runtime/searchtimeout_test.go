package runtime

import (
	"context"
	"testing"
	"time"
)

func TestSearchTimeoutArg(t *testing.T) {
	cases := map[string]struct {
		args map[string]any
		want time.Duration
	}{
		"absent":            {map[string]any{"query": "x"}, 0},
		"seconds as number": {map[string]any{"timeout_s": float64(90)}, 90 * time.Second},
		"seconds as int":    {map[string]any{"timeout_s": 45}, 45 * time.Second},
		"duration string":   {map[string]any{"timeout_s": "2m"}, 2 * time.Minute},
		// A builder model often emits the number as a string; treating that as
		// unparseable would silently fall back to the old 30s ceiling.
		"bare number string": {map[string]any{"timeout_s": "90"}, 90 * time.Second},
		"alias timeout":      {map[string]any{"timeout": "45s"}, 45 * time.Second},
		"alias seconds":      {map[string]any{"timeout_seconds": float64(20)}, 20 * time.Second},
		"zero ignored":       {map[string]any{"timeout_s": float64(0)}, 0},
		"negative ignored":   {map[string]any{"timeout_s": -5}, 0},
		"garbage ignored":    {map[string]any{"timeout_s": "soon"}, 0},
		"nil ignored":        {map[string]any{"timeout_s": nil}, 0},
	}
	for name, tc := range cases {
		if got := searchTimeoutArg(tc.args); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

func TestSearchTimeoutFor_Precedence(t *testing.T) {
	e := &Engine{}

	// 4. Nothing set anywhere → the historical default, so existing installs
	//    behave exactly as before.
	if got := e.searchTimeoutFor(context.Background(), nil); got != DefaultSearchTimeout {
		t.Errorf("default: got %v want %v", got, DefaultSearchTimeout)
	}

	// 3. Operator config.
	e.SetSearchTimeout(90 * time.Second)
	if got := e.searchTimeoutFor(context.Background(), nil); got != 90*time.Second {
		t.Errorf("config: got %v want 90s", got)
	}

	// 2. Per-node tool timeout on the context beats operator config — this is
	//    the case that used to be impossible: a node declaring timeout: 3m still
	//    died at the hardcoded 30s.
	ctx := WithToolTimeout(context.Background(), 3*time.Minute)
	if got := e.searchTimeoutFor(ctx, nil); got != 3*time.Minute {
		t.Errorf("node timeout: got %v want 3m", got)
	}

	// 1. The call's own argument is the most specific and wins over both.
	if got := e.searchTimeoutFor(ctx, map[string]any{"timeout_s": float64(15)}); got != 15*time.Second {
		t.Errorf("arg: got %v want 15s", got)
	}
}

func TestSearchTimeoutClamping(t *testing.T) {
	e := &Engine{}
	// A typo ("6000" meaning 60s) must not park a worker for 100 minutes.
	if got := e.searchTimeoutFor(context.Background(), map[string]any{"timeout_s": float64(6000)}); got != MaxSearchTimeout {
		t.Errorf("clamp: got %v want %v", got, MaxSearchTimeout)
	}
	e.SetSearchTimeout(24 * time.Hour)
	if got := e.getSearchTimeout(); got != MaxSearchTimeout {
		t.Errorf("config clamp: got %v want %v", got, MaxSearchTimeout)
	}
	e.SetSearchTimeout(0)
	if got := e.getSearchTimeout(); got != DefaultSearchTimeout {
		t.Errorf("zero resets to default: got %v", got)
	}
}

func TestParseSearchTimeout(t *testing.T) {
	ok := map[string]time.Duration{
		"90s":  90 * time.Second,
		"2m":   2 * time.Minute,
		"90":   90 * time.Second, // bare seconds, what operators reach for first
		" 45 ": 45 * time.Second,
	}
	for raw, want := range ok {
		got, valid := ParseSearchTimeout(raw)
		if !valid || got != want {
			t.Errorf("%q: got (%v,%v) want (%v,true)", raw, got, valid, want)
		}
	}
	// Invalid input must REPORT invalid rather than silently applying a default,
	// so the operator learns why their setting had no effect.
	for _, raw := range []string{"", "  ", "soon", "90 seconds", "-5s", "0"} {
		if _, valid := ParseSearchTimeout(raw); valid {
			t.Errorf("%q: expected invalid", raw)
		}
	}
	// Over-large values parse but clamp.
	if got, valid := ParseSearchTimeout("24h"); !valid || got != MaxSearchTimeout {
		t.Errorf("24h: got (%v,%v) want (%v,true)", got, valid, MaxSearchTimeout)
	}
}
