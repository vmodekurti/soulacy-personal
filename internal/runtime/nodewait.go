// nodewait.go — derive a flow node's execution timeout from the wait it declares.
//
// A long-poll node tells us, in its own arguments, how long it expects to run
// (e.g. NotebookLM `research_status` with `max_wait: 1200`). The engine bounds
// each tool call by the global runtime.tool_timeout (default 120s), so such a
// node would die at 2 minutes. nodeExecTimeout lets the RUNTIME honor that
// declared wait directly — an explicit FlowNode.Timeout wins, else the largest
// wait/timeout argument (+ headroom) is used — so the node works on a plain run,
// not only after Studio's build pre-processing. Returns 0 when nothing applies
// (keep the global default).
package runtime

import (
	"encoding/json"
	"strings"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// nodeWaitArgKeys are argument names that denote "how long this call may run", in
// SECONDS, across common MCP/tool conventions.
var nodeWaitArgKeys = map[string]bool{
	"max_wait": true, "maxwait": true, "max_wait_s": true, "max_wait_seconds": true,
	"wait": true, "wait_s": true, "wait_seconds": true,
	"timeout": true, "timeout_s": true, "timeout_sec": true, "timeout_seconds": true,
	"poll_timeout": true, "poll_timeout_s": true, "deadline_s": true,
}

// nodeWaitHeadroom is added to a node's declared wait so the engine budget
// strictly exceeds the tool's own internal wait.
const nodeWaitHeadroom = 60 * time.Second

// nodeExecTimeout returns the timeout for this node's tool/python call: an
// explicit, valid FlowNode.Timeout if set; otherwise one derived from the largest
// wait/timeout argument the node declares (in its rendered input or its params),
// plus headroom; otherwise 0 (use the global default).
func nodeExecTimeout(node sdkr.FlowNode, renderedInput string) time.Duration {
	if t := strings.TrimSpace(node.Timeout); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			return d
		}
	}
	sec := 0
	if obj := decodeJSONObject(renderedInput); obj != nil {
		for k, v := range obj {
			if nodeWaitArgKeys[strings.ToLower(strings.TrimSpace(k))] {
				if s := waitSecondsOf(v); s > sec {
					sec = s
				}
			}
		}
	}
	for k, v := range node.Params {
		if nodeWaitArgKeys[strings.ToLower(strings.TrimSpace(k))] {
			if s := waitSecondsOf(v); s > sec {
				sec = s
			}
		}
	}
	if sec > 0 {
		return time.Duration(sec)*time.Second + nodeWaitHeadroom
	}
	if strings.TrimSpace(node.Tool) == "kb_write" {
		return defaultKBWriteFlowTimeout
	}
	// Slow-by-design external tools: an MCP call in a flow talks to an outside
	// service and routinely does real work that exceeds the tight global
	// tool_timeout (default 120s) — e.g. NotebookLM's research_import. Give such a
	// node a generous default so a legitimately-slow call isn't cut off at 2
	// minutes, WITHOUT the developer setting a timeout on every node by hand. Fast
	// calls are unaffected (they return immediately regardless of the budget); the
	// run/agent budget still bounds a genuinely stuck call.
	if strings.HasPrefix(strings.TrimSpace(node.Tool), "mcp__") {
		return defaultMCPFlowTimeout
	}
	return 0
}

// defaultMCPFlowTimeout is the per-call budget for an external MCP tool in a flow
// when it declares no timeout of its own — generous because external operations
// are slow and variable, while local builtins keep the tight global default.
const defaultMCPFlowTimeout = 10 * time.Minute

// defaultKBWriteFlowTimeout keeps Knowledge writes from inheriting an
// operator-wide tool timeout meant for long external MCP jobs. Small text
// ingests complete quickly; large or unhealthy embedder writes should fail
// visibly and let Studio self-heal instead of making the chat look stuck.
const defaultKBWriteFlowTimeout = 2 * time.Minute

// decodeJSONObject parses a JSON object string, or returns nil.
func decodeJSONObject(s string) map[string]any {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return nil
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// waitSecondsOf coerces a wait value to whole seconds. Numbers are seconds; a
// string is parsed as a Go duration ("20m") then as a bare number ("1200").
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
		if d, err := time.ParseDuration(s + "s"); err == nil && d > 0 {
			return int(d.Seconds())
		}
	}
	return 0
}
