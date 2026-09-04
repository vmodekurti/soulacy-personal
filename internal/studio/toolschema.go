package studio

// toolschema.go — capture tool contracts at save, and detect drift afterwards
// (P0-3).
//
// Capture answers "what did this workflow agree to". Drift answers "has that
// agreement changed", and — critically — WHICH node and WHICH field, so the
// report is actionable rather than a bare "the tool changed".
//
// The distinction that makes drift worth detecting at all: a validation error
// against a tool whose schema is unchanged means the workflow was always wrong,
// and the fix is in the workflow. The same error against a tool whose schema
// moved means the workflow was right when it was built, and the fix may be
// upstream. Without a snapshot the two are indistinguishable, which is why a
// 07:00 failure used to start with an hour of "but this worked yesterday".

import (
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// CaptureToolSchemas records the live contract of every tool the draft's flow
// calls. now is injected so a save is reproducible in tests.
func CaptureToolSchemas(flow Flow, cat Catalog, now time.Time) *agent.ToolSchemaSnapshot {
	// tool name → node ids that call it.
	callers := map[string][]string{}
	for _, n := range flow.Nodes {
		tool := strings.TrimSpace(n.Tool)
		if tool == "" || sdkr.IsStructuralKind(n.Kind) {
			continue
		}
		callers[tool] = append(callers[tool], n.ID)
	}
	if len(callers) == 0 {
		return nil
	}

	live := liveToolParams(cat)
	snap := &agent.ToolSchemaSnapshot{CapturedAt: now.UTC().Format(time.RFC3339)}
	names := make([]string, 0, len(callers))
	for tool := range callers {
		names = append(names, tool)
	}
	sort.Strings(names) // deterministic output: a re-save must not churn the file
	for _, tool := range names {
		params, known := live[tool]
		if !known {
			// A tool with no discoverable schema (a builtin without a hint, or a
			// server that was down at save) is deliberately NOT recorded: an
			// empty hash would later read as "the schema changed to nothing".
			continue
		}
		nodes := append([]string(nil), callers[tool]...)
		sort.Strings(nodes)
		snap.Tools = append(snap.Tools, agent.ToolSchemaRecord{
			Tool:   tool,
			Hash:   agent.HashToolSignature(params),
			Params: params,
			Nodes:  nodes,
		})
	}
	if len(snap.Tools) == 0 {
		return nil
	}
	return snap
}

// liveToolParams indexes the catalog's current param hints by exact tool name.
func liveToolParams(cat Catalog) map[string]string {
	out := map[string]string{}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if name := strings.TrimSpace(t.Name); name != "" {
				out[name] = t.Params
			}
		}
	}
	for name, params := range builtinToolParams() {
		if _, taken := out[name]; !taken {
			out[name] = toolParamSignature(params)
		}
	}
	return out
}

// toolParamSignature renders builtin params in the same compact form MCP hints
// use, so both hash through one code path.
func toolParamSignature(params []ToolParam) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		name := p.Name
		if p.Required {
			name += "*"
		}
		if p.Type != "" {
			name += ":" + p.Type
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// ToolDrift is one tool whose contract moved since the agent was built.
type ToolDrift struct {
	Tool string `json:"tool"`
	// Nodes are the workflow blocks that call it — what the operator must look at.
	Nodes []string `json:"nodes,omitempty"`
	// Was / Now are the recorded and current signatures.
	Was string `json:"was,omitempty"`
	Now string `json:"now,omitempty"`
	// Kind is "changed" | "removed".
	Kind string `json:"kind"`
	// Fields names the exact parameters that differ, which is the part that
	// makes a drift report fixable rather than merely alarming.
	Fields []string `json:"fields,omitempty"`
	// Breaking marks drift that can invalidate existing calls: a parameter
	// removed, renamed, or newly required. A purely additive optional parameter
	// is reported but not breaking, because every existing call still validates.
	Breaking bool   `json:"breaking"`
	Detail   string `json:"detail"`
}

// DetectToolDrift compares an agent's captured snapshot against the live
// catalog. Returns nil when nothing moved (or when there is no snapshot to
// compare, which is the case for agents saved before P0-3).
func DetectToolDrift(snap *agent.ToolSchemaSnapshot, cat Catalog) []ToolDrift {
	if !snap.HasTools() {
		return nil
	}
	live := liveToolParams(cat)
	var out []ToolDrift
	for _, rec := range snap.Tools {
		params, present := live[rec.Tool]
		if !present {
			out = append(out, ToolDrift{
				Tool: rec.Tool, Nodes: rec.Nodes, Was: rec.Params, Kind: "removed", Breaking: true,
				Detail: "this tool is no longer offered by any connected server — the block that calls it cannot run",
			})
			continue
		}
		if agent.HashToolSignature(params) == rec.Hash {
			continue
		}
		fields, breaking := diffToolSignatures(rec.Params, params)
		out = append(out, ToolDrift{
			Tool: rec.Tool, Nodes: rec.Nodes, Was: rec.Params, Now: params,
			Kind: "changed", Fields: fields, Breaking: breaking,
			Detail: driftDetail(fields, breaking),
		})
	}
	return out
}

func driftDetail(fields []string, breaking bool) string {
	if len(fields) == 0 {
		return "the tool's signature changed"
	}
	what := strings.Join(fields, ", ")
	if breaking {
		return "the tool's contract changed in a way that can break existing calls: " + what
	}
	return "the tool gained optional parameters: " + what
}

// diffToolSignatures reports which parameters differ between two compact
// signatures, and whether the change can invalidate an existing call.
//
// Breaking = a parameter disappeared (removed or renamed), or an existing
// parameter became required, or its type changed. Purely additive OPTIONAL
// parameters are not breaking: every call that worked yesterday still validates.
func diffToolSignatures(was, now string) (fields []string, breaking bool) {
	oldParams := indexParams(parseToolParams(was))
	newParams := indexParams(parseToolParams(now))

	names := map[string]bool{}
	for n := range oldParams {
		names[n] = true
	}
	for n := range newParams {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		o, hadOld := oldParams[name]
		n, hasNew := newParams[name]
		switch {
		case hadOld && !hasNew:
			fields = append(fields, name+" (removed)")
			breaking = true
		case !hadOld && hasNew:
			if n.Required {
				fields = append(fields, name+" (added, required)")
				breaking = true
			} else {
				fields = append(fields, name+" (added, optional)")
			}
		case o.Required != n.Required:
			if n.Required {
				fields = append(fields, name+" (now required)")
				breaking = true
			} else {
				fields = append(fields, name+" (no longer required)")
			}
		case !strings.EqualFold(o.Type, n.Type):
			fields = append(fields, name+" ("+orUnknown(o.Type)+" → "+orUnknown(n.Type)+")")
			breaking = true
		}
	}
	return fields, breaking
}

func indexParams(ps []ToolParam) map[string]ToolParam {
	out := make(map[string]ToolParam, len(ps))
	for _, p := range ps {
		out[strings.ToLower(strings.TrimSpace(p.Name))] = p
	}
	return out
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "untyped"
	}
	return s
}

// NeedsRecertification reports whether detected drift is severe enough to
// invalidate a prior certification. Breaking drift does; additive drift is
// surfaced but does not by itself revoke a certificate, because nothing the
// agent already does has stopped being valid.
func NeedsRecertification(drift []ToolDrift) bool {
	for _, d := range drift {
		if d.Breaking {
			return true
		}
	}
	return false
}
