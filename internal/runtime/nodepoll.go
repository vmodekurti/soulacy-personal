// nodepoll.go — auto-poll async work so Studio, not the developer, handles the
// "wait until it's ready" plumbing.
//
// Many real tools are asynchronous: a call kicks the work off and returns a
// "still in progress" status; you then poll a status endpoint until it's done
// (NotebookLM audio/video generation, long research, exports, builds). Wiring
// that by hand — a poll loop, a sleep step, a max_wait the tool may not even
// accept — is exactly the plumbing the studio should hide. So when a STATUS/POLL
// node returns an in-progress result, the flow runtime re-polls it on an interval
// until it reaches a terminal state (done or failed) or a budget runs out. The
// re-call is the same idempotent status check; side-effecting nodes (create/start)
// never match the poll pattern and are never re-invoked.
package runtime

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// pollNodeNameRe matches a tool whose name marks it as an idempotent status/poll
// check (research_status, studio_status, *_poll, *_check, *_wait). Create/start
// tools never match, so they are never auto-re-invoked.
var pollNodeNameRe = regexp.MustCompile(`(?:^|_)(status|poll|wait|check)(?:$|_)`)

// isPollNode reports whether a node is a safe-to-repeat status/poll check.
func isPollNode(node sdkr.FlowNode) bool {
	name := strings.TrimSpace(node.Tool)
	if name == "" {
		return false
	}
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:] // strip the mcp__<server>__ prefix
	}
	return pollNodeNameRe.MatchString(strings.ToLower(name))
}

// pendingStatusWords are values that mean "the async work hasn't finished".
var pendingStatusWords = map[string]bool{
	"in_progress": true, "inprogress": true, "in-progress": true, "pending": true,
	"running": true, "processing": true, "generating": true, "queued": true,
	"working": true, "started": true, "not_ready": true, "notready": true, "waiting": true,
}

// pendingCountKeys are numeric counters that, when > 0, mean work is outstanding.
var pendingCountKeys = map[string]bool{
	"in_progress": true, "inprogress": true, "pending": true,
	"processing": true, "running": true, "queued": true,
}

// resultPending reports whether a tool result still represents in-progress work:
// any status-like string in a pending state, or any positive in-progress counter,
// anywhere in the (possibly nested) JSON.
func resultPending(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false // unparseable → treat as terminal (don't loop blindly)
	}
	return anyPending(v)
}

func anyPending(v any) bool {
	switch t := v.(type) {
	case string:
		return pendingStatusWords[strings.ToLower(strings.TrimSpace(t))]
	case map[string]any:
		for k, val := range t {
			if pendingCountKeys[strings.ToLower(strings.TrimSpace(k))] {
				if n, ok := val.(float64); ok && n > 0 {
					return true
				}
			}
			if anyPending(val) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if anyPending(e) {
				return true
			}
		}
	}
	return false
}

const (
	defaultPollBudget   = 10 * time.Minute
	defaultPollInterval = 15 * time.Second
)

// pollBudget is the total time to keep polling a node: an explicit FlowNode.Timeout,
// else a declared wait/timeout argument (max_wait, timeout_s, …), else 10 minutes.
func pollBudget(node sdkr.FlowNode, renderedInput string) time.Duration {
	if t := strings.TrimSpace(node.Timeout); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			return d
		}
	}
	if sec := nodeArgSeconds(node, renderedInput, nodeWaitArgKeys); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return defaultPollBudget
}

// pollIntervalKeys override the delay between polls.
var pollIntervalKeys = map[string]bool{
	"poll_interval": true, "poll_interval_s": true, "interval": true, "interval_s": true,
}

// pollInterval is the delay between polls: a declared interval argument, else 15s.
func pollInterval(node sdkr.FlowNode, renderedInput string) time.Duration {
	if sec := nodeArgSeconds(node, renderedInput, pollIntervalKeys); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return defaultPollInterval
}

// nodeArgSeconds finds the largest value (seconds) among keys, in the rendered
// input object or the node params.
func nodeArgSeconds(node sdkr.FlowNode, renderedInput string, keys map[string]bool) int {
	best := 0
	consider := func(k string, v any) {
		if keys[strings.ToLower(strings.TrimSpace(k))] {
			if s := waitSecondsOf(v); s > best {
				best = s
			}
		}
	}
	if obj := decodeJSONObject(renderedInput); obj != nil {
		for k, v := range obj {
			consider(k, v)
		}
	}
	for k, v := range node.Params {
		consider(k, v)
	}
	return best
}

// autoPollNode polls `first`'s node until terminal or the node's budget elapses,
// using the node-derived budget/interval.
func autoPollNode(ctx context.Context, node sdkr.FlowNode, renderedInput string, first []byte, recall func(context.Context) ([]byte, error)) ([]byte, error) {
	return autoPoll(ctx, first, pollBudget(node, renderedInput), pollInterval(node, renderedInput), recall)
}

// autoPoll re-invokes recall on `interval` until the result is terminal or
// `budget` elapses. When the budget is hit while still pending, the last result
// is returned (not an error) so a downstream branch can decide what to do with an
// unfinished artifact. Honors context cancellation.
func autoPoll(ctx context.Context, first []byte, budget, interval time.Duration, recall func(context.Context) ([]byte, error)) ([]byte, error) {
	if !resultPending(first) {
		return first, nil
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := time.Now().Add(budget)
	result := first
	for resultPending(result) {
		if !time.Now().Before(deadline) {
			return result, nil // budget exhausted; return the last (still-pending) state
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(interval):
		}
		next, err := recall(ctx)
		if err != nil {
			return next, err
		}
		result = next
	}
	return result, nil
}
