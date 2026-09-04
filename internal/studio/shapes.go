package studio

import (
	"encoding/json"
	"strings"
)

// shapes.go — Phase D: the dry-run playground's feedback loop. After a run, the
// REAL output of each node is captured as a compact "shape" (keys + truncated
// sample values) and offered back to the per-node compiler (CompileNode's
// UpstreamVar.Shape). This is what makes "no code" reliable: instead of the model
// guessing a tool's payload format, it compiles the next step against data the
// user actually saw.

// maxShapeLen bounds a captured shape so the grounding stays compact and long
// string values (which could carry sensitive content) are truncated, not echoed
// in full.
const maxShapeLen = 240

// CaptureShape renders a compact, length-bounded JSON sample of a node's output,
// suitable for grounding the per-node compiler. Objects/arrays keep their
// structure (so field names survive) but long string leaves are truncated.
// Returns "" for empty/unparseable output.
func CaptureShape(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Non-JSON output: return a truncated raw sample.
		return truncate(s, maxShapeLen)
	}
	v = redactLeaves(v, 0)
	b, err := json.Marshal(v)
	if err != nil {
		return truncate(s, maxShapeLen)
	}
	return truncate(string(b), maxShapeLen)
}

// redactLeaves walks a parsed-JSON value and truncates long string leaves so a
// captured shape conveys structure + a sample without echoing large/sensitive
// blobs. Depth is bounded to keep deeply nested outputs compact.
func redactLeaves(v any, depth int) any {
	switch t := v.(type) {
	case string:
		return truncate(t, 60)
	case map[string]any:
		if depth >= 4 {
			return "{…}"
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = redactLeaves(val, depth+1)
		}
		return out
	case []any:
		if depth >= 4 {
			return "[…]"
		}
		// Keep just the first element as a representative sample of the shape.
		if len(t) == 0 {
			return t
		}
		return []any{redactLeaves(t[0], depth+1)}
	default:
		return v
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ShapesFromTrace builds the per-var output shapes from a run trace: for each
// executed node that has an output var, it captures the node's real output shape.
// Used to populate TestResult.Shapes and, in turn, ground CompileNode. Nodes with
// no output var or empty output are skipped. Deterministic order (trace order).
func ShapesFromTrace(draft Draft, trace []TraceEntry) []UpstreamVar {
	// node id -> output var name
	outVar := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			outVar[n.ID] = v
		}
	}
	var out []UpstreamVar
	seen := map[string]bool{}
	for _, e := range trace {
		name := outVar[e.NodeID]
		if name == "" || seen[name] {
			continue
		}
		shape := CaptureShape(e.Output)
		if shape == "" {
			continue
		}
		seen[name] = true
		out = append(out, UpstreamVar{Name: name, Shape: shape})
	}
	return out
}
