// validate.go — Studio's "validate" step (Story M3). The canvas calls this
// while the user edits a draft graph to get fast, structured feedback: hard
// errors (the graph won't compile) and soft warnings (the graph compiles but
// has a smell, e.g. an unreachable node or an unknown trigger type).
//
// The logic lives here (not in the gateway) so it is unit-testable without an
// HTTP server: POST /api/v1/studio/validate is a thin wrapper over Validate.
// Validate NEVER returns a Go error — a bad graph is data, reported via the
// result's Errors. This is the deterministic contract the frontend pins:
//
//	validate.request{workflow} ->
//	  validate.response{ ok, errors[{nodeId?,edgeIndex?,message}], warnings[{nodeId?,message}] }
package studio

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// quotedWholeTemplateRe matches a JSON string value that is exactly one template
// action ("{{ … }}"); neutralized to "_" so a templated tool input parses as JSON
// for argument-key extraction.
var quotedWholeTemplateRe = regexp.MustCompile(`"\{\{[^{}]*\}\}"`)

// ValidateToolArgs flags a tool node that passes an argument the tool's schema
// does NOT accept — the "unexpected keyword argument" class (e.g. num_results on a
// NotebookLM research tool). It checks ONLY MCP tools that publish their parameter
// names in the catalog (so there are no false positives for tools whose schema we
// can't see), and only ever WARNS (the runtime is still the source of truth).
// Catalog-less callers get nil. Pure + deterministic.
func ValidateToolArgs(draft Draft, cat Catalog) []ValidateWarning {
	// tool name (normalized) -> accepted param-name set, for tools that publish one.
	accepts := map[string]map[string]bool{}
	for _, srv := range cat.MCP {
		for _, tl := range srv.Tools {
			name := strings.TrimSpace(tl.Name)
			if name == "" || strings.TrimSpace(tl.Params) == "" {
				continue
			}
			accepts[normalizeName(name)] = paramNameSet(tl.Params)
		}
	}
	if len(accepts) == 0 {
		return nil
	}
	var out []ValidateWarning
	for _, n := range draft.Flow.Nodes {
		t := strings.TrimSpace(n.Tool)
		if t == "" {
			continue
		}
		ps, ok := accepts[normalizeName(t)]
		if !ok || len(ps) == 0 {
			continue
		}
		declaredPort := map[string]bool{}
		for _, p := range n.Inputs {
			declaredPort[p.Name] = true
		}
		for _, k := range inputArgKeys(n.Input) {
			if ps[k] || declaredPort[k] {
				continue // valid arg, or supplied via a typed input port
			}
			out = append(out, ValidateWarning{
				NodeID: n.ID,
				Message: fmt.Sprintf("Argument %q isn't accepted by tool %q (accepts: %s). Remove it or use a valid argument.",
					k, t, strings.Join(sortedKeySet(ps), ", ")),
			})
		}
		// A typed input PORT must also name a real tool argument — otherwise the
		// wired value binds to a key the tool will reject at run time. The bind key
		// is the port's Field override when set, else its Name. (Validating port
		// wires against the real schema, not just template keys.)
		for _, p := range n.Inputs {
			key := p.Name
			if p.Field != "" {
				key = p.Field
			}
			if key == "" || ps[key] {
				continue
			}
			out = append(out, ValidateWarning{
				NodeID: n.ID,
				Message: fmt.Sprintf("Input port %q binds to an argument tool %q doesn't accept (accepts: %s). Rewire it to a valid argument.",
					key, t, strings.Join(sortedKeySet(ps), ", ")),
			})
		}
	}
	return out
}

// paramNameSet parses a compact param hint ("title*:string, summary:string") into
// the set of bare parameter names {title, summary}.
func paramNameSet(hint string) map[string]bool {
	set := map[string]bool{}
	for _, part := range strings.Split(hint, ",") {
		p := strings.TrimSpace(part)
		if i := strings.IndexByte(p, ':'); i >= 0 {
			p = p[:i]
		}
		p = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p), "*"))
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// inputArgKeys extracts the top-level argument keys from a tool node's JSON input
// template. Templates are neutralized so the JSON parses; a non-object or
// unparseable input yields no keys (so we never warn on something we can't read).
func inputArgKeys(input string) []string {
	s := strings.TrimSpace(input)
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	s = quotedWholeTemplateRe.ReplaceAllString(s, `"_"`) // "{{…}}" -> "_"
	s = templateActionRe.ReplaceAllString(s, "null")     // any remaining {{…}} -> null
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedKeySet returns a set's keys sorted, for stable messages.
func sortedKeySet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// subprocessCallRe detects a Python step shelling out to an external command.
var subprocessCallRe = regexp.MustCompile(`\b(?:subprocess\.(?:run|call|Popen|check_output|check_call)|os\.system|os\.popen)\b`)

// shelledBinRe pulls the command name out of a subprocess/os.system call, e.g.
// subprocess.run(['nlm', …]) or os.system("nlm notebook …") -> "nlm".
var shelledBinRe = regexp.MustCompile(`(?:subprocess\.\w+\(\s*\[\s*|os\.system\(\s*|os\.popen\(\s*)["']([A-Za-z0-9_./-]+)`)

// shellSmellWarnings flags a python node that shells out to a CLI — the brittle
// pattern behind the recurring "guessed the wrong --flag" failures. When the SAME
// workflow already drives an MCP (other nodes call mcp__<server>__…), the nudge
// is specific: use that MCP's typed tools here instead of the CLI. A warning, not
// a blocker — shelling out is sometimes the only option, but it should be a
// deliberate choice, not an accident the author can't see.
// shellSmellIssues enforces the rule "Python is for data glue, not for calling
// tools": a python node that shells out to a CLL is a BLOCKER when the workflow
// already drives an MCP (that operation belongs in a typed tool/MCP node), and a
// WARNING otherwise (shelling out with no available tool is sometimes the only
// option, but should be a deliberate choice). Returns (blockers, warnings).
func shellSmellIssues(d Draft) (blockers []ValidateError, warnings []ValidateWarning) {
	mcpServers := map[string]bool{}
	for _, n := range d.Flow.Nodes {
		t := strings.TrimSpace(n.Tool)
		if strings.HasPrefix(strings.ToLower(t), "mcp__") {
			if parts := strings.Split(t, "__"); len(parts) >= 2 {
				mcpServers[strings.ToLower(parts[1])] = true
			}
		}
	}
	for _, n := range d.Flow.Nodes {
		if n.Kind != sdkr.FlowNodePython || strings.TrimSpace(n.Code) == "" {
			continue
		}
		if !subprocessCallRe.MatchString(n.Code) {
			continue
		}
		bin := ""
		if m := shelledBinRe.FindStringSubmatch(n.Code); m != nil {
			bin = m[1]
			if i := strings.LastIndexByte(bin, '/'); i >= 0 {
				bin = bin[i+1:]
			}
		}
		msg := "This Python step shells out to a command-line tool"
		if bin != "" {
			msg += " (`" + bin + "`)"
		}
		msg += ". Python is for data glue (parsing/formatting) only — calling a tool/MCP or a CLI that wraps one must be a typed tool node."
		if len(mcpServers) > 0 {
			names := make([]string, 0, len(mcpServers))
			for s := range mcpServers {
				names = append(names, s)
			}
			sort.Strings(names)
			blockers = append(blockers, ValidateError{
				NodeID: n.ID,
				Message: msg + " This workflow already uses the " + strings.Join(names, ", ") +
					" MCP — replace this step with that MCP's typed tool node(s).",
			})
			continue
		}
		warnings = append(warnings, ValidateWarning{
			NodeID:  n.ID,
			Message: msg + " If a tool or MCP exposes this operation, use it instead.",
		})
	}
	return blockers, warnings
}

// ValidateError is one hard problem that stops the graph compiling. NodeID
// and/or EdgeIndex locate it on the canvas when they can be attributed; the
// Message is always set. EdgeIndex is a pointer so 0 ("edge 0") is
// distinguishable from "not an edge problem" (nil).
type ValidateError struct {
	NodeID    string `json:"nodeId,omitempty"`
	EdgeIndex *int   `json:"edgeIndex,omitempty"`
	Message   string `json:"message"`
	// Source names the check that produced this error, when it is not the
	// graph structure itself. Validate() folds in the completion-contract
	// rules so a single Validate call sees everything — but the contract
	// report ALSO runs those rules directly, so without this tag the same
	// sentence appeared twice on the Save step under two different headings,
	// each with its own generic remedy. Worse, the graph-integrity copy said
	// "fix the broken graph structure" about a graph that compiles fine; the
	// real problem was a missing delivery channel.
	Source string `json:"source,omitempty"`
}

// ValidateSourceCompletion marks an error raised by the completion-contract
// rules rather than by graph validation.
const ValidateSourceCompletion = "completion"

// ValidateWarning is one soft problem: the graph still compiles, but the
// shape is suspicious. NodeID locates it when applicable.
type ValidateWarning struct {
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
}

// ValidateResult is the POST /api/v1/studio/validate response. Ok is true iff
// there are no hard errors; warnings never affect Ok.
type ValidateResult struct {
	Ok       bool              `json:"ok"`
	Errors   []ValidateError   `json:"errors"`
	Warnings []ValidateWarning `json:"warnings"`
}

// knownTriggerTypes is the closed set the compiler/normalizer recognizes; any
// other (non-empty) trigger.type earns a soft warning.
var knownTriggerTypes = map[string]bool{
	"chat": true, "schedule": true, "channel": true, "webhook": true, "manual": true,
}

// Validate runs reasoning.CompileFlow on the draft's flow and collects the
// outcome as structured data. It never returns an error: a graph that fails
// to compile yields Ok=false with one (best-attributed) ValidateError; a
// graph that compiles yields Ok=true plus any soft warnings.
//
// Errors (hard — block compile):
//   - whatever CompileFlow rejects (missing ids, dangling/bad-target edges,
//     undeclared ports, bad entry, unknown kind/on_error, …), attributed to a
//     node or edge when the message lets us.
//
// Warnings (soft — graph compiles):
//   - a node with no incoming edge that is not the entry (unreachable);
//   - an unknown/unrecognized trigger.type.
func Validate(draft Draft) ValidateResult {
	res := ValidateResult{
		Ok:       true,
		Errors:   []ValidateError{},
		Warnings: []ValidateWarning{},
	}

	if !draft.IsAgent() {
		if _, err := reasoning.CompileFlow(draft.spec()); err != nil {
			res.Ok = false
			res.Errors = append(res.Errors, attributeFlowError(draft.Flow, err))
			// A graph that doesn't compile: report the hard error only. Soft
			// warnings about reachability are unreliable on an invalid graph.
			return res
		}

		res.Warnings = append(res.Warnings, flowWarnings(draft.Flow)...)
		res.Warnings = append(res.Warnings, nodeTimeoutWarnings(draft.Flow)...)
		res.Warnings = append(res.Warnings, systemToolWarnings(draft.Flow)...)
	}
	res.Warnings = append(res.Warnings, triggerWarnings(draft.Trigger)...)
	outputErrors, outputWarnings := outputContractValidateIssues(draft)
	if len(outputErrors) > 0 {
		res.Ok = false
		res.Errors = append(res.Errors, outputErrors...)
	}
	res.Warnings = append(res.Warnings, outputWarnings...)
	completionErrors, completionWarnings := completionContractValidateIssues(draft)
	if len(completionErrors) > 0 {
		res.Ok = false
		res.Errors = append(res.Errors, completionErrors...)
	}
	res.Warnings = append(res.Warnings, completionWarnings...)
	// "Python is for data glue, not for calling tools": a python step shelling out
	// to a CLI is a blocker when an MCP is available (use the tool), else a warning.
	shellBlocks, shellWarns := shellSmellIssues(draft)
	if len(shellBlocks) > 0 {
		res.Ok = false
		res.Errors = append(res.Errors, shellBlocks...)
	}
	res.Warnings = append(res.Warnings, shellWarns...)

	// Phase A: trigger/exit endpoint-block authoring checks (no-op unless the
	// user wired endpoint blocks). A "block"-severity issue (e.g. two triggers)
	// fails the graph; "warn" issues are advisory.
	for _, iss := range ValidateEndpoints(&draft) {
		if iss.Severity == "block" {
			res.Ok = false
			res.Errors = append(res.Errors, ValidateError{NodeID: iss.Node, Message: iss.Message})
		} else {
			res.Warnings = append(res.Warnings, ValidateWarning{NodeID: iss.Node, Message: iss.Message})
		}
	}
	return res
}

func outputContractValidateIssues(draft Draft) ([]ValidateError, []ValidateWarning) {
	var errs []ValidateError
	var warns []ValidateWarning
	checkOutputContracts(draft, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			errs = append(errs, ValidateError{NodeID: node, Message: msg})
			return
		}
		warns = append(warns, ValidateWarning{NodeID: node, Message: msg})
	})
	return errs, warns
}

// gatedSystemTools are the SEC-3 "SYSTEM" built-ins (arbitrary code / host
// mutation). They are NOT in a workflow agent's runnable toolset unless the agent
// declares system access AND the server allows it — so a flow tool node calling
// one fails at run time with a cryptic "tool not found". Mirrors the runtime's
// privilegedSystemTools plus python_eval/shell_exec (the code-execution glue).
var gatedSystemTools = map[string]bool{
	"python_eval": true, "shell_exec": true, "shell": true, "run_script": true,
	"install_library": true, "write_file": true, "download_file": true,
}

// systemToolWarnings flags a workflow tool node that calls a gated system tool.
// Surfaces the real problem at DESIGN time — instead of a "tool not found" loop
// during Build — and points the developer at the right alternative.
func systemToolWarnings(f Flow) []ValidateWarning {
	var out []ValidateWarning
	for _, n := range f.Nodes {
		t := strings.TrimSpace(n.Tool)
		if !gatedSystemTools[t] {
			continue
		}
		hint := "For data glue (parsing/formatting), use a Custom Python block instead; for autonomous tool/skill routing, a reasoning agent fits better."
		out = append(out, ValidateWarning{
			NodeID:  n.ID,
			Message: "This step calls the system tool \"" + t + "\", which a workflow agent can't run unless system-tool access is explicitly enabled. " + hint,
		})
	}
	return out
}

// nodeTimeoutWarnings flags any node whose Timeout override isn't a valid Go
// duration ("30s", "10m") — so a typo that would be silently ignored at run time
// surfaces in "fix before saving" instead. An empty Timeout is fine (use global).
func nodeTimeoutWarnings(f Flow) []ValidateWarning {
	var out []ValidateWarning
	for _, n := range f.Nodes {
		t := strings.TrimSpace(n.Timeout)
		if t == "" {
			continue
		}
		if d, err := time.ParseDuration(t); err != nil || d <= 0 {
			out = append(out, ValidateWarning{
				NodeID:  n.ID,
				Message: fmt.Sprintf("Timeout %q isn't a valid duration (use e.g. \"30s\", \"5m\"); the global tool_timeout will be used instead.", n.Timeout),
			})
		}
	}
	return out
}

// attributeFlowError turns a CompileFlow error into a structured ValidateError,
// best-effort attaching a nodeId or edgeIndex parsed from the message. The
// message text is the source of truth (CompileFlow's messages are stable and
// quote the offending id/index); when nothing can be attributed we still
// surface the precise message.
func attributeFlowError(flow Flow, err error) ValidateError {
	msg := err.Error()
	ve := ValidateError{Message: msg}

	// Edge-shaped errors: "flow: edge <i> ...". Pull the index out so the
	// canvas can highlight the offending edge.
	if idx, ok := parseEdgeIndex(msg); ok {
		ve.EdgeIndex = &idx
		// Edge errors that quote a target/source node id also carry a nodeId.
		if id := firstQuotedKnownNode(msg, flow); id != "" {
			ve.NodeID = id
		}
		return ve
	}

	// Node-shaped errors: "flow: node <i> ..." or "flow: node \"id\" ...".
	if id := firstQuotedKnownNode(msg, flow); id != "" {
		ve.NodeID = id
		return ve
	}
	// "flow: entry node \"x\" does not exist" quotes an id that is (by
	// definition) NOT a known node — still surface it as the nodeId so the
	// canvas can point at the entry selector.
	if strings.Contains(msg, "entry node") {
		if id := firstQuoted(msg); id != "" {
			ve.NodeID = id
		}
	}
	return ve
}

// parseEdgeIndex extracts <i> from "flow: edge <i> ..." messages.
func parseEdgeIndex(msg string) (int, bool) {
	const marker = "edge "
	i := strings.Index(msg, marker)
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	n := 0
	for k := 0; k < j; k++ {
		n = n*10 + int(rest[k]-'0')
	}
	return n, true
}

// firstQuotedKnownNode returns the first double-quoted token in msg that names
// a node actually present in the flow (so we attribute to a real node, not to
// a tool name or a missing-entry id).
func firstQuotedKnownNode(msg string, flow Flow) string {
	ids := map[string]bool{}
	for _, n := range flow.Nodes {
		ids[n.ID] = true
	}
	rest := msg
	for {
		q := firstQuoted(rest)
		if q == "" {
			return ""
		}
		if ids[q] {
			return q
		}
		// Advance past this quoted token and keep looking.
		idx := strings.Index(rest, `"`+q+`"`)
		if idx < 0 {
			return ""
		}
		rest = rest[idx+len(q)+2:]
	}
}

// firstQuoted returns the contents of the first "double-quoted" token in s, or
// "" when there is none.
func firstQuoted(s string) string {
	i := strings.IndexByte(s, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i+1:], '"')
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

// flowWarnings flags nodes that have no incoming edge yet are not the entry —
// they are unreachable, which usually means the author forgot to wire them.
func flowWarnings(flow Flow) []ValidateWarning {
	if len(flow.Nodes) == 0 {
		return nil
	}
	entry := flow.Entry
	if entry == "" {
		entry = flow.Nodes[0].ID
	}
	hasIncoming := map[string]bool{}
	for _, e := range flow.Edges {
		if !flowTerminal(e.To) {
			hasIncoming[e.To] = true
		}
	}
	var out []ValidateWarning
	for _, n := range flow.Nodes {
		if n.ID == entry {
			continue
		}
		if !hasIncoming[n.ID] {
			out = append(out, ValidateWarning{
				NodeID:  n.ID,
				Message: fmt.Sprintf("node %q has no incoming edge and is not the entry node (unreachable)", n.ID),
			})
		}
	}
	// Custom Python node sanity ("python" == sdkr.FlowNodePython): inline code
	// should define a run(inputs) entry point. (A python node with NEITHER code
	// nor a tool is already a hard CompileFlow error, surfaced as a validate
	// error, so it never reaches here.) Warning only — never blocks the canvas.
	for _, n := range flow.Nodes {
		if n.Kind != "python" {
			continue
		}
		code := strings.TrimSpace(n.Code)
		if code != "" && !strings.Contains(code, "def run(") {
			out = append(out, ValidateWarning{
				NodeID:  n.ID,
				Message: fmt.Sprintf("python node %q should define a run(inputs) function — its return value becomes the node output", n.ID),
			})
		}
	}
	return out
}

// triggerWarnings flags a non-empty trigger.type the compiler does not
// recognize. An empty type is left to the compiler's clarify flow (not a
// validate-time warning).
func triggerWarnings(t Trigger) []ValidateWarning {
	typ := strings.ToLower(strings.TrimSpace(t.Type))
	if typ == "" || knownTriggerTypes[typ] {
		return nil
	}
	return []ValidateWarning{{
		Message: fmt.Sprintf("unknown trigger type %q (expected one of: chat, schedule, channel, webhook, manual)", t.Type),
	}}
}

// flowTerminal mirrors reasoning's terminal-edge check ("" or "end").
func flowTerminal(to string) bool { return to == "" || to == "end" }
