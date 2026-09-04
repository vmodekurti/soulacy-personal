package runtime

// searchtimeout.go resolves the HTTP client timeout for the built-in
// web_search tool.
//
// Every search backend used to hardcode `http.Client{Timeout: 30 * time.Second}`.
// That was a silent ceiling: a flow node could declare `timeout: 60s`, the
// walker would honour it on the context, and the search STILL died at exactly
// 30s with "context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)" — because the client's own timeout fired first and no surface
// exposed it. A slow provider, a cold index, or a fan-out hammering the same
// endpoint therefore failed with an error that looked like the node's fault and
// could not be fixed from config, CLI, or GUI.
//
// Precedence, most specific first:
//
//  1. the call's own `timeout_s` argument (per-node, set in the inspector),
//  2. the per-node tool-timeout carried on the context (FlowNode.Timeout),
//  3. the operator's `search.timeout` in config.yaml (`sy config set`),
//  4. DefaultSearchTimeout.
//
// A caller-supplied deadline already on the context still applies on top: this
// only decides the CLIENT's ceiling, never extends a deadline the runtime set.

import (
	"context"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
)

// DefaultSearchTimeout is the web_search HTTP timeout when nothing overrides
// it — the historical hardcoded value, kept so existing installs are unchanged.
const DefaultSearchTimeout = 30 * time.Second

// MaxSearchTimeout caps what any surface can request, so a typo
// (`timeout_s: 6000`) parks a worker for a hundred minutes instead of failing.
const MaxSearchTimeout = 10 * time.Minute

// SetSearchTimeout sets the operator-level web_search HTTP timeout. d<=0
// restores DefaultSearchTimeout. Values above MaxSearchTimeout are clamped.
func (e *Engine) SetSearchTimeout(d time.Duration) {
	e.searchProviderMu.Lock()
	defer e.searchProviderMu.Unlock()
	e.searchTimeout = clampSearchTimeout(d)
}

// getSearchTimeout returns the configured operator-level timeout.
func (e *Engine) getSearchTimeout() time.Duration {
	e.searchProviderMu.RLock()
	defer e.searchProviderMu.RUnlock()
	if e.searchTimeout <= 0 {
		return DefaultSearchTimeout
	}
	return e.searchTimeout
}

// searchTimeoutFor resolves the effective client timeout for one web_search
// call, applying the precedence documented above.
func (e *Engine) searchTimeoutFor(ctx context.Context, args map[string]any) time.Duration {
	if d := searchTimeoutArg(args); d > 0 {
		return clampSearchTimeout(d)
	}
	if d, ok := ctx.Value(toolTimeoutOverrideKey{}).(time.Duration); ok && d > 0 {
		return clampSearchTimeout(d)
	}
	return e.getSearchTimeout()
}

// searchTimeoutArg reads a per-call timeout from the tool arguments. Accepts
// `timeout_s` / `timeout` as a number of seconds, or a Go duration string
// ("45s", "2m") — builder models and hand-authored nodes both occur, and a
// rejected spelling would silently fall back to the old ceiling.
func searchTimeoutArg(args map[string]any) time.Duration {
	for _, key := range []string{"timeout_s", "timeout", "timeout_seconds"} {
		raw, ok := args[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case float64:
			if v > 0 {
				return time.Duration(v * float64(time.Second))
			}
		case int:
			if v > 0 {
				return time.Duration(v) * time.Second
			}
		case int64:
			if v > 0 {
				return time.Duration(v) * time.Second
			}
		case string:
			s := strings.TrimSpace(v)
			if s == "" {
				continue
			}
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
			// A bare number in a string ("45") means seconds.
			if d, err := time.ParseDuration(s + "s"); err == nil && d > 0 {
				return d
			}
		}
	}
	return 0
}

// clampSearchTimeout bounds a requested timeout into the supported range.
func clampSearchTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultSearchTimeout
	}
	if d > MaxSearchTimeout {
		return MaxSearchTimeout
	}
	return d
}

// ParseSearchTimeout parses an operator-supplied `search.timeout` and clamps it
// into the supported range. Thin wrapper over config.ParseSearchTimeout, which
// is the shared parse the CLI wizard uses too — one spelling authority, one
// clamp authority.
func ParseSearchTimeout(raw string) (time.Duration, bool) {
	d, ok := config.ParseSearchTimeout(raw)
	if !ok {
		return 0, false
	}
	return clampSearchTimeout(d), true
}
