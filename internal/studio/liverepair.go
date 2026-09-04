package studio

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template/parse"
)

// liverepair.go turns a Run Live trace into concrete, reviewable repair
// proposals. The premise: a node often breaks not because the flow is wrong but
// because a REAL API returned a shape the node didn't expect (`results` vs
// `data.items`, a JSON string instead of an object, an empty list). Every node
// run already captures its rendered input, the actual output bytes, and the
// error (reasoning.FlowNodeRun); this file classifies what went wrong, diagnoses
// the shape mismatch deterministically where it can, and otherwise hands the
// model the real sample to rewrite exactly one node. Nothing here APPLIES a
// change — it returns proposals for approval (see ApplyProposal).

// LiveNodeRun is the studio-facing view of one executed node from a live trace.
// The gateway builds it from reasoning.FlowNodeRun plus the draft (to recover
// each node's Output var name), keeping this package decoupled from the runtime.
type LiveNodeRun struct {
	NodeID    string          `json:"node_id"`
	Kind      string          `json:"kind"`
	Input     string          `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
	OutputVar string          `json:"output_var,omitempty"` // node.Output (the flow var it wrote)
}

// RepairClass is why a node failed, deciding which repair strategy applies.
type RepairClass string

const (
	RepairNone          RepairClass = "none"
	RepairShapeDrift    RepairClass = "shape_drift"    // real data shape != what the node assumed
	RepairTemplateError RepairClass = "template_error" // bad funcname / syntax in the input template
	RepairEmptyResult   RepairClass = "empty_result"   // upstream produced an empty collection
	RepairToolFailure   RepairClass = "tool_failure"   // auth/4xx/network — not a formatting fix
)

// RepairProposal is a single suggested change to one node, presented for
// approval. Auto=true marks a low-risk deterministic adapter that may be
// auto-applied when the user opts into unattended repair; LLM rewrites and any
// python code change are always Auto=false (review required).
type RepairProposal struct {
	NodeID    string      `json:"node_id"`
	Field     string      `json:"field"` // "input" | "code"
	Class     RepairClass `json:"class"`
	Old       string      `json:"old"`
	New       string      `json:"new"`
	Rationale string      `json:"rationale"`
	Auto      bool        `json:"auto"`
	// ObservedKeys are the top-level keys actually seen in the upstream output,
	// so the UI can show "the API returned {results,meta}" next to the diff.
	ObservedKeys []string `json:"observed_keys,omitempty"`
}

// Classify decides why a node run failed from its error text and output. It is
// intentionally conservative: only shape_drift / template_error are treated as
// auto-repairable classes; tool_failure is surfaced advisory-only.
func Classify(run LiveNodeRun) RepairClass {
	// A node can fail two ways: a hard Go-level error, OR it "ran" (✓) but its
	// OUTPUT reports an error — e.g. a python script that caught a json.loads
	// failure and returned {"error": "..."}. Both are repairable.
	e := strings.TrimSpace(run.Error)
	if e == "" {
		e = outputErrorText(run.Output)
	}
	if e == "" {
		return RepairNone
	}
	el := strings.ToLower(e)

	// Template funcmap / syntax problems (caught at render, before the tool ran).
	// Keep the generic "template:" marker out of this first pass: Go includes it
	// in field-shape errors too, and those need upstream-shape diagnosis.
	for _, s := range []string{"function \"", "not defined", "unexpected \"", "unterminated", "unclosed"} {
		if strings.Contains(el, s) {
			return RepairTemplateError
		}
	}
	// Shape mismatches: the classic string-vs-object and key-not-found signals
	// from Go templates and from python parsing real API output — plus the JSON
	// decode failures a script hits when the input isn't the shape it assumed
	// (e.g. an HTTP response with headers before the JSON body).
	for _, s := range []string{
		"can't evaluate field", "range can't iterate", "can't give argument",
		"keyerror", "typeerror", "indexerror", "has no attribute",
		"not subscriptable", "nonetype", "is not a", "expected",
		"expecting value", "expecting property", "json", "decode", "char 0", "no such key",
	} {
		if strings.Contains(el, s) {
			return RepairShapeDrift
		}
	}
	if strings.Contains(el, "template:") {
		return RepairTemplateError
	}
	// Auth / HTTP / network: a real failure the user must fix, not a reshape.
	for _, s := range []string{"401", "403", "404", "429", "500", "unauthorized", "forbidden", "timeout", "no such host", "connection refused"} {
		if strings.Contains(el, s) {
			return RepairToolFailure
		}
	}
	return RepairToolFailure
}

// ShapeDiagnosis is the deterministic read of a shape_drift failure: which
// template field-paths the failing node referenced, what the upstream output
// actually contained, and (when derivable) a concrete remap suggestion.
type ShapeDiagnosis struct {
	ProducerVar    string
	ReferencedKeys []string // e.g. ["results"] the node tried to read under ProducerVar
	ObservedKeys   []string // top-level keys really present in the producer output
	ArrayKey       string   // a present key whose value is an array (remap target)
	StringWrapped  bool     // producer output was a JSON string that itself holds JSON
}

// commonArrayAliases are the keys real APIs bury their list of items under, in
// rough priority order. Used to remap a missing reference onto what's present.
var commonArrayAliases = []string{"results", "items", "data", "rows", "entries", "records", "list", "values", "articles", "hits", "docs"}

// Diagnose inspects a failing node against the trace to explain a shape drift.
// It resolves the node's referenced field-paths, finds the producing run whose
// OutputVar the node read, and compares the assumed shape to the real one.
func Diagnose(runs []LiveNodeRun, target LiveNodeRun) ShapeDiagnosis {
	var d ShapeDiagnosis
	chains := templateFieldChains(target.Input)
	if len(chains) == 0 {
		return d
	}
	// Index producer outputs by the var they wrote.
	byVar := map[string]LiveNodeRun{}
	for _, r := range runs {
		if r.OutputVar != "" {
			byVar[r.OutputVar] = r
		}
	}
	for _, chain := range chains {
		if len(chain) == 0 {
			continue
		}
		v := chain[0]
		prod, ok := byVar[v]
		if !ok || len(prod.Output) == 0 {
			continue
		}
		d.ProducerVar = v
		if len(chain) > 1 {
			d.ReferencedKeys = append(d.ReferencedKeys, chain[1])
		}
		// Is the producer output a JSON string that actually wraps JSON?
		if s, wrapped := stringWrappedJSON(prod.Output); wrapped {
			d.StringWrapped = true
			if obj := decodeObject(json.RawMessage(s)); obj != nil {
				d.ObservedKeys = sortedObjKeys(obj)
				d.ArrayKey = firstArrayKey(obj)
			}
			continue
		}
		if obj := decodeObject(prod.Output); obj != nil {
			d.ObservedKeys = sortedObjKeys(obj)
			d.ArrayKey = firstArrayKey(obj)
		}
	}
	return d
}

// ProposeAdapter synthesizes a deterministic, low-risk fix for a shape drift
// when the mismatch is unambiguous (a string-wrapped payload, or a referenced
// key that's absent while a sibling array key is present). Returns ok=false when
// no confident deterministic fix exists — the caller then tries the LLM.
func ProposeAdapter(target LiveNodeRun, d ShapeDiagnosis) (RepairProposal, bool) {
	if target.Field() == "" || d.ProducerVar == "" {
		return RepairProposal{}, false
	}
	// Case A: producer returned a JSON *string*; wrap references in fromJson so
	// field access/range work against the parsed value.
	if d.StringWrapped {
		newInput := rewriteVarWithFromJSON(target.Input, d.ProducerVar)
		if newInput != target.Input {
			return RepairProposal{
				NodeID: target.NodeID, Field: "input", Class: RepairShapeDrift,
				Old: target.Input, New: newInput, Auto: true, ObservedKeys: d.ObservedKeys,
				Rationale: fmt.Sprintf("Upstream var %q is a JSON string, not an object; parse it with fromJson before field access.", d.ProducerVar),
			}, true
		}
	}
	// Case B: the node read `.<var>.<key>` but the real output has no <key>,
	// while a sibling array key IS present — remap onto the present key.
	if d.ArrayKey != "" && len(d.ReferencedKeys) > 0 {
		want := d.ReferencedKeys[0]
		if want != d.ArrayKey && !strIn(d.ObservedKeys, want) {
			oldRef := "." + d.ProducerVar + "." + want
			newRef := "." + d.ProducerVar + "." + d.ArrayKey
			if strings.Contains(target.Input, oldRef) {
				return RepairProposal{
					NodeID: target.NodeID, Field: "input", Class: RepairShapeDrift,
					Old: target.Input, New: strings.ReplaceAll(target.Input, oldRef, newRef),
					Auto: true, ObservedKeys: d.ObservedKeys,
					Rationale: fmt.Sprintf("The API response has no %q; the list is under %q. Remapped %s → %s.", want, d.ArrayKey, oldRef, newRef),
				}, true
			}
		}
	}
	return RepairProposal{}, false
}

// Field reports which draft field a live run's node repairs edit: python nodes
// with inline code repair "code"; everything else repairs the "input" template.
func (r LiveNodeRun) Field() string {
	if r.Kind == "python" {
		return "code"
	}
	if strings.TrimSpace(r.Input) != "" {
		return "input"
	}
	return ""
}

// ---- template + JSON helpers (deterministic, no LLM) ----

// templateFieldChains extracts the dotted field paths referenced in a Go
// template (e.g. "{{ range .search.results }}" → [["search","results"]]). It
// parses the template AST so it's robust to funcs/pipes, and silently returns
// nothing for an unparseable template (that's a template_error, handled apart).
func templateFieldChains(tmpl string) [][]string {
	if strings.TrimSpace(tmpl) == "" {
		return nil
	}
	trees, err := parse.Parse("t", tmpl, "", "", FlowFuncNamesAsMap())
	if err != nil {
		return nil
	}
	tree := trees["t"]
	if tree == nil || tree.Root == nil {
		return nil
	}
	var out [][]string
	seen := map[string]bool{}
	var walk func(n parse.Node)
	add := func(idents []string) {
		if len(idents) == 0 {
			return
		}
		key := strings.Join(idents, ".")
		if !seen[key] {
			seen[key] = true
			out = append(out, idents)
		}
	}
	walk = func(n parse.Node) {
		switch v := n.(type) {
		case *parse.ListNode:
			if v == nil {
				return
			}
			for _, c := range v.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walkPipe(v.Pipe, add)
		case *parse.IfNode:
			walkPipe(v.Pipe, add)
			walk(v.List)
			walk(v.ElseList)
		case *parse.RangeNode:
			walkPipe(v.Pipe, add)
			walk(v.List)
			walk(v.ElseList)
		case *parse.WithNode:
			walkPipe(v.Pipe, add)
			walk(v.List)
			walk(v.ElseList)
		}
	}
	walk(tree.Root)
	return out
}

func walkPipe(p *parse.PipeNode, add func([]string)) {
	if p == nil {
		return
	}
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			switch a := arg.(type) {
			case *parse.FieldNode:
				add(a.Ident)
			case *parse.ChainNode:
				add(a.Field)
			case *parse.PipeNode:
				walkPipe(a, add)
			}
		}
	}
}

// FlowFuncNamesAsMap returns the flow funcmap as a name→placeholder map suitable
// for parse.Parse (which only needs the set of defined function names).
func FlowFuncNamesAsMap() map[string]any {
	// Mirror the reasoning funcset names so template parsing accepts them.
	names := []string{
		"toJson", "json", "fromJson", "fromjson", "parseJson", "parseJSON", "unmarshal",
		"default", "join", "first", "last", "pluck", "dict",
		"now", "today", "nowUnix", "dateFmt", "dateFormat", "formatDate", "date",
	}
	m := make(map[string]any, len(names))
	for _, n := range names {
		m[n] = func() string { return "" }
	}
	return m
}

func stringWrappedJSON(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	t := strings.TrimSpace(s)
	if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		return t, true
	}
	return "", false
}

// outputErrorText extracts an error a node reported in its OUTPUT despite
// "succeeding" (no Go error) — the soft-failure case. It reads a top-level
// "error"/"errors"/"err" string field. Empty/whitespace values (a script that
// sets error:"" on success) don't count. This is what lets Studio notice that
// parse_stock_data returned {"error":"Expecting value: line 1 column 1"} even
// though the node ran green.
func outputErrorText(raw json.RawMessage) string {
	obj := decodeObject(raw)
	if obj == nil {
		return ""
	}
	for _, k := range []string{"error", "errors", "err"} {
		if v, ok := obj[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// EffectiveError is the hard error if present, else the soft output-reported
// error. It's what the repair pipeline reasons about.
func EffectiveError(run LiveNodeRun) string {
	if e := strings.TrimSpace(run.Error); e != "" {
		return e
	}
	return outputErrorText(run.Output)
}

func decodeObject(raw json.RawMessage) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func firstArrayKey(obj map[string]any) string {
	// Prefer well-known aliases, then any array-valued key (stable order).
	for _, k := range commonArrayAliases {
		if v, ok := obj[k]; ok {
			if _, isArr := v.([]any); isArr {
				return k
			}
		}
	}
	keys := sortedObjKeys(obj)
	for _, k := range keys {
		if _, isArr := obj[k].([]any); isArr {
			return k
		}
	}
	return ""
}

func sortedObjKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// rewriteVarWithFromJSON turns bare references to a var (`.var` / `.var.x`) into
// fromJson-parsed references (`(fromJson .var)` / `(fromJson .var).x`) so a
// string-wrapped JSON payload becomes addressable. Conservative: only rewrites
// the exact producer var, leaves other vars untouched.
func rewriteVarWithFromJSON(tmpl, v string) string {
	// `.v.` (field access) → parse then access.
	out := strings.ReplaceAll(tmpl, "."+v+".", " (fromJson ."+v+").")
	// Standalone `.v` at a boundary (range/print) → parsed value.
	out = strings.ReplaceAll(out, " ."+v+" ", " (fromJson ."+v+") ")
	return out
}

// nodeCode returns the inline python code of the draft node with id, or "".
func nodeCode(d Draft, id string) string {
	for _, n := range d.Flow.Nodes {
		if n.ID == id {
			return n.Code
		}
	}
	return ""
}

func strIn(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ApplyProposal patches the draft node named in the proposal, returning false if
// the node isn't found or the field is unknown. Callers re-validate afterward
// (NormalizeAndCheck / CompileFlow) before persisting.
func ApplyProposal(d *Draft, p RepairProposal) bool {
	for i := range d.Flow.Nodes {
		if d.Flow.Nodes[i].ID != p.NodeID {
			continue
		}
		switch p.Field {
		case "input":
			d.Flow.Nodes[i].Input = p.New
		case "code":
			// A code change invalidates any prior per-node python consent stamp
			// (bound to the old code hash); clear it so the runtime re-gates.
			d.Flow.Nodes[i].Code = p.New
			d.Flow.Nodes[i].Consent = nil
		default:
			return false
		}
		return true
	}
	return false
}
