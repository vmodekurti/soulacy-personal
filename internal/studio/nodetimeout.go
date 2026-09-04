// nodetimeout.go — auto-derive a block's execution timeout from the wait it
// already declares.
//
// A long-poll node (e.g. NotebookLM `research_status` with `max_wait: 1200`)
// tells us, in its own arguments, how long it expects to run. Yet the engine
// bounds every tool call by the global runtime.tool_timeout (default 120s), so
// such a node dies at 2 minutes even though it asked to poll for 20. Rather than
// make the developer also fill in the per-node Timeout by hand, Studio reads the
// wait the node already carries and sets the block's Timeout to match (plus
// headroom so the engine budget strictly exceeds the tool's own wait). It only
// fills an EMPTY Timeout — an explicit one always wins — and is idempotent.
package studio

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// waitArgKeys are the argument names that denote "how long this call may run",
// in SECONDS, across common MCP/tool conventions.
var waitArgKeys = map[string]bool{
	"max_wait": true, "maxwait": true, "max_wait_s": true, "max_wait_seconds": true,
	"wait": true, "wait_s": true, "wait_seconds": true,
	"timeout": true, "timeout_s": true, "timeout_sec": true, "timeout_seconds": true,
	"poll_timeout": true, "poll_timeout_s": true, "deadline_s": true,
}

// nodeTimeoutHeadroom is added to a node's declared wait so the engine's budget
// strictly exceeds the tool's own internal wait (avoids racing the deadline).
const nodeTimeoutHeadroom = 60

// deriveNodeTimeouts sets each executable node's Timeout from the largest
// wait/timeout argument it carries (in input or params), when the node hasn't
// declared a Timeout of its own. Returns the number of nodes updated.
func deriveNodeTimeouts(draft *Draft) int {
	if draft == nil {
		return 0
	}
	updated := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if sdkr.IsStructuralKind(n.Kind) || strings.TrimSpace(n.Timeout) != "" {
			continue
		}
		if sec := declaredWaitSeconds(n); sec > 0 {
			n.Timeout = fmt.Sprintf("%ds", sec+nodeTimeoutHeadroom)
			updated++
		}
	}
	return updated
}

// declaredWaitSeconds returns the largest wait/timeout value (seconds) the node
// declares in its input JSON object or its params, or 0 if none.
func declaredWaitSeconds(n *sdkr.FlowNode) int {
	best := 0
	consider := func(key string, v any) {
		if !waitArgKeys[strings.ToLower(strings.TrimSpace(key))] {
			return
		}
		if s := waitSecondsOf(v); s > best {
			best = s
		}
	}
	if obj, ok := decodeInputObject(n.Input); ok {
		for k, v := range obj {
			consider(k, v)
		}
	}
	for k, v := range n.Params {
		consider(k, v)
	}
	return best
}

// waitSecondsOf coerces a wait value to whole seconds. Numbers are seconds; a
// string is parsed as a Go duration first ("20m") then as a bare number ("1200").
func waitSecondsOf(v any) int {
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return int(t)
		}
	case int:
		if t > 0 {
			return t
		}
	case int64:
		if t > 0 {
			return int(t)
		}
	case json.Number:
		if f, err := t.Float64(); err == nil && f > 0 {
			return int(f)
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return int(d.Seconds())
		}
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err == nil && f > 0 {
			return int(f)
		}
	}
	return 0
}
