package studio

import (
	"encoding/json"
	"regexp"
	"strings"
	"text/template"

	"github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// preflightTemplateFuncs uses the EXACT function set the flow renderer
// registers (internal/reasoning), so "valid at save" matches "renders at run" —
// a template using any supported helper (toJson, pluck, default, …) parses
// here, and one using an unsupported function is correctly flagged.
var preflightTemplateFuncs = reasoning.FlowTemplateFuncs()

// checkTemplates validates every node Input and edge predicate as a Go template
// (the renderer parses these at run time). A malformed template — an extra dot
// like {{ ..date_info }}, an unclosed {{, a bad pipeline — is reported as a
// blocker WITH the parse error, so it's caught at save instead of failing
// mid-run (e.g. a scheduled agent at 7am).
func checkTemplates(draft Draft, add func(sev, kind, node, msg, fix string)) {
	parse := func(s string) error {
		if !strings.Contains(s, "{{") {
			return nil
		}
		_, err := template.New("").Funcs(preflightTemplateFuncs).Option("missingkey=zero").Parse(s)
		return err
	}
	for _, n := range draft.Flow.Nodes {
		if err := parse(n.Input); err != nil {
			add("block", "template", n.ID, "This step's input has an invalid template: "+cleanTemplateErr(err), "Fix the {{ … }} expression (e.g. a stray extra dot or unclosed braces).")
		}
	}
	for i, e := range draft.Flow.Edges {
		if err := parse(e.If); err != nil {
			add("block", "template", "", "Edge "+itoa(i+1)+" ("+e.From+"→"+e.To+") has an invalid condition: "+cleanTemplateErr(err), "Fix the {{ … }} predicate on that edge.")
		}
	}
}

// cleanTemplateErr trims Go's verbose "template: :1:" prefix to the useful part.
func cleanTemplateErr(err error) string {
	m := err.Error()
	if i := strings.LastIndex(m, ": "); i >= 0 && i+2 < len(m) {
		return m[i+2:]
	}
	return m
}

// PreflightInput is the live environment a draft is checked against before
// saving. It is supplied by the caller (the gateway, from authoritative
// server-side state) so Preflight itself stays a pure, deterministic, testable
// function with no I/O.
type PreflightInput struct {
	// Catalog is the grounded catalog (skills, MCP servers + tools + param
	// hints, agents, tools, channels, KBs) the draft was built against.
	Catalog Catalog
	// ConnectedMCP maps an MCP server name to whether it is currently connected.
	// A tool node calling mcp__<server>__<tool> on a server not in this set (or
	// mapped false) is a blocker — the agent cannot run.
	ConnectedMCP map[string]bool
	// SecretsSet maps a secret/credential name to whether a value is stored.
	// A nil map means "the caller did not supply credential state" and NOTHING
	// is judged against it — the alternative (treating nil as "nothing is set")
	// would manufacture a blocker for every caller that never had a vault.
	SecretsSet map[string]bool
	// ChannelsConfigured maps a channel id to whether it is configured + enabled.
	ChannelsConfigured map[string]bool

	// ── Append-only fields (ST-07/ST-08). Every one of these is zero-value
	// compatible: nil/empty means "the caller didn't supply this", and Preflight
	// must then stay silent about it. A caller that upgrades without passing new
	// state must never start seeing blockers it cannot act on. ────────────────

	// RequiredSecrets are the exact credentials this draft needs, supplied by
	// the caller who alone knows how a provider/channel maps onto a vault key
	// (same reasoning as CertificationInput.RequiredSecrets). When nil,
	// Preflight falls back to DeriveSecretRequirements(draft) so readiness stops
	// reporting green with every API key missing — the bug this field fixes.
	RequiredSecrets []SecretRequirement
	// ProvidersAvailable maps an LLM provider id to whether it is configured and
	// usable for a RUN. This is the agent's runtime provider, not the Studio
	// builder model that handleStudioModelAdvice judges — an agent can be built
	// by a healthy builder model and then be unrunnable because its own provider
	// has no key. nil = unknown, no provider verdict is emitted.
	ProvidersAvailable map[string]bool
	// ModelsAvailable maps a model id (bare "gpt-4o", or "provider/model" when
	// the caller wants provider-scoped identity) to whether that model is
	// currently servable. nil = unknown, no model verdict is emitted.
	ModelsAvailable map[string]bool
}

// SecretRequirement names one credential the draft needs and what needs it.
// Kind mirrors the preflight issue vocabulary ("provider" | "channel" | "tool" |
// "mcp") so the client can group the fix, and Owner is the concrete thing that
// requires it (the provider id, channel id, MCP server, or tool name).
type SecretRequirement struct {
	// Name is the EXACT credential name as stored — it is echoed verbatim into
	// the Fix, because "add the missing credential" without the key name is an
	// instruction nobody can follow.
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	Owner string `json:"owner,omitempty"`
}

// PreflightIssue is one finding from validating a draft. Severity is "block"
// (saving should be refused or loudly gated), "warn" (saving is fine but the
// agent may not run until the user fixes setup), or "pass" (the check ran and
// succeeded — kept so a client can render the full Ready/Warning/Blocker
// triage instead of a list that only ever shows what is wrong). Fix is a short,
// plain-language remediation step (Story #12).
type PreflightIssue struct {
	Severity string `json:"severity"` // block | warn | pass
	Kind     string `json:"kind"`     // tool|agent|mcp|schedule|secret|channel|field|dependency|provider|model
	NodeID   string `json:"nodeId,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
	// Action is a machine-readable remediation token from certify.go's
	// vocabulary ("open_providers", "open_mcp", "open_delivery",
	// "choose_model", "open_studio", …). A prose Fix can only be read; an
	// Action can be rendered as a button that lands the user on the exact
	// screen. Deliberately the SAME vocabulary as CertRequirement.Action —
	// two competing token sets would guarantee the GUI handles one of them
	// wrong. Every blocker carries one.
	Action string `json:"action,omitempty"`
	// ActionParams carries the operands the button needs ("secret", "server",
	// "channel", "provider", "model"), so the client does not have to
	// re-parse the human message to find out WHICH thing to open.
	ActionParams map[string]string `json:"actionParams,omitempty"`
}

// PreflightResult is the consolidated validation report (Stories #11 + #12):
// every blocker and warning in one structured list, plus an OK flag that is
// true only when there are no blockers.
type PreflightResult struct {
	OK       bool             `json:"ok"`
	Blockers []PreflightIssue `json:"blockers"`
	Warnings []PreflightIssue `json:"warnings"`
	// Passes are the checks that RAN and succeeded. Preflight used to compute a
	// pass/warn/block tier and then throw the pass tier away, so a client could
	// only ever render "what is broken" — leaving "was this even checked?"
	// indistinguishable from "this is fine". Appended field: existing callers
	// that only read Blockers/Warnings are unaffected.
	Passes []PreflightIssue `json:"passes,omitempty"`
}

// Preflight validates a draft against the live environment and returns a single
// consolidated report. It is pure and deterministic. It complements (does not
// replace) reasoning.CompileFlow, which checks graph STRUCTURE; Preflight checks
// the things that only matter at save/run time: that referenced capabilities
// exist and are connected, required tool arguments are actually filled, a
// scheduled agent has a usable schedule, and the credentials/channels the agent
// depends on are present.
func Preflight(draft Draft, in PreflightInput) PreflightResult {
	var res PreflightResult
	// record is the one place an issue is filed, so the Action token can never
	// be forgotten: it is derived from Kind when the caller doesn't pass a more
	// specific one. That makes "every blocker is actionable" a structural
	// property instead of a convention each check site has to remember.
	record := func(issue PreflightIssue) {
		if issue.Action == "" {
			issue.Action = actionForKind(issue.Kind)
		}
		switch issue.Severity {
		case "block":
			res.Blockers = append(res.Blockers, issue)
		case "pass":
			res.Passes = append(res.Passes, issue)
		default:
			res.Warnings = append(res.Warnings, issue)
		}
	}
	add := func(sev, kind, node, msg, fix string) {
		record(PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
	}
	pass := func(kind, msg string) {
		record(PreflightIssue{Severity: "pass", Kind: kind, Message: msg})
	}

	toolSet := lowerSet(in.Catalog.Tools)
	agentSet := catalogAgentSet(in.Catalog)
	// Index MCP tools by full name → required arg names, for arg checks.
	mcpToolReq := map[string][]string{}
	mcpToolKnown := map[string]bool{}
	builtinToolReq := builtinRequiredToolArgs()
	for _, srv := range in.Catalog.MCP {
		for _, t := range srv.Tools {
			full := strings.TrimSpace(t.Name)
			if full == "" {
				continue
			}
			mcpToolKnown[full] = true
			mcpToolReq[full] = requiredParams(t.Params)
		}
	}

	for _, n := range draft.Flow.Nodes {
		switch n.Kind {
		case "agent":
			id := strings.TrimSpace(n.Agent)
			if id == "" {
				add("block", "field", n.ID, "Agent node has no agent assigned.", "Pick or define an agent for this step.")
				continue
			}
			// A non-catalog agent is fine ONLY if the draft defines it in
			// new_agents (the save path stubs it). Otherwise it's a dangling ref.
			if !agentSet[strings.ToLower(id)] && !hasNewAgent(draft, id) {
				add("warn", "agent", n.ID, "References agent \""+id+"\" which does not exist yet.", "It will be created as a helper agent on save; review its prompt afterwards.")
			}
		case "tool":
			tool := strings.TrimSpace(n.Tool)
			if tool == "" {
				add("block", "field", n.ID, "Tool node has no tool selected.", "Choose a tool for this step.")
				continue
			}
			if strings.HasPrefix(tool, "mcp__") {
				server := mcpServerOf(tool)
				if server != "" && !mcpConnected(in.ConnectedMCP, server) {
					add("block", "mcp", n.ID, "Uses MCP tool \""+tool+"\" but its server \""+server+"\" is not connected.", "Connect the \""+server+"\" MCP server in Settings, then re-save.")
				}
				// Required-argument check: every required arg of the MCP tool must
				// be present and non-placeholder in the node's input — OR supplied by
				// a typed input PORT (the template-free handoff). Without the port
				// check, a node whose arg was lowered to a port reads as "empty" and
				// the build loop repairs it forever (the LLM re-adds the arg, the
				// portizer re-wires it). Port name OR Field may carry the arg key.
				for _, want := range mcpToolReq[tool] {
					if argFilled(n.Input, want) || nodeSuppliesViaPort(draft.Flow, n, want) {
						continue
					}
					add("block", "dependency", n.ID, "Required argument \""+want+"\" for \""+tool+"\" is empty or a placeholder.", "Provide a real value, or feed it from an upstream step's output.")
				}
			} else if len(toolSet) > 0 && !toolSet[strings.ToLower(tool)] && !isBuiltinContractTool(tool) {
				// Builtin/unknown tool not in the grounded catalog: warn (the
				// catalog may be partial), don't block.
				add("warn", "tool", n.ID, "Tool \""+tool+"\" is not in the available tools list.", "Confirm the tool name is correct, or install the capability it needs.")
			}
			if reqs := builtinToolReq[tool]; len(reqs) > 0 {
				if strings.TrimSpace(n.Input) != "" && !strings.HasPrefix(strings.TrimSpace(n.Input), "{") {
					add("block", "dependency", n.ID, "Tool \""+tool+"\" expects JSON object arguments, but this step passes raw/free-form text.", "Wrap the value in the tool's required JSON fields, or insert an extraction/transform step.")
					continue
				}
				for _, want := range reqs {
					if builtinArgFilled(draft.Flow, n, want) {
						continue
					}
					add("block", "dependency", n.ID, "Required argument \""+want+"\" for \""+tool+"\" is empty or a placeholder.", "Provide a real value, or feed it from an upstream step's output.")
				}
			}
		case "python":
			// A Custom Python node MUST define a top-level `def run(inputs)` —
			// the harness calls run(args). Without it the node fails at run time
			// with "name 'run' is not defined". Block at save so a broken python
			// node can never reach the scheduler and fail unattended at 7am.
			if !definesRunEntrypoint(n.Code) {
				add("block", "field", n.ID, "Custom Python node has no top-level `def run(inputs):` entry point.", "Define `def run(inputs):` (the workflow calls run with the node's inputs).")
			}
		}
	}

	// ReAct/Plan-Execute agent: the Flow is empty, so validate the tool allowlist
	// directly — every MCP tool's server must be connected, every builtin should
	// be known.
	if draft.IsAgent() {
		for _, t := range draft.Tools {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "mcp__") {
				server := mcpServerOf(t)
				if server != "" && !mcpConnected(in.ConnectedMCP, server) {
					add("block", "mcp", "", "Agent uses MCP tool \""+t+"\" but its server \""+server+"\" is not connected.", "Connect the \""+server+"\" MCP server in Settings, then re-save.")
				}
				if known := mcpToolKnown; len(known) > 0 && !known[t] {
					add("warn", "tool", "", "Agent lists MCP tool \""+t+"\" which isn't exposed by any connected server.", "Confirm the exact tool name from the MCP server.")
				}
			} else if len(toolSet) > 0 && !toolSet[strings.ToLower(t)] {
				add("warn", "tool", "", "Agent lists tool \""+t+"\" which isn't in the available tools list.", "Confirm the tool name is correct, or install the capability it needs.")
			}
		}
		if len(draft.Tools) == 0 && len(draft.NewAgents) == 0 {
			add("block", "field", "", "This agent has no tools or peer agents to act with.", "Add at least one tool the agent can call.")
		}
	}

	// Schedule validity (Story #11): a scheduled agent needs a usable cron.
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "schedule") {
		if !validCron(cronOf(draft.Trigger)) {
			add("block", "schedule", "", "This is a scheduled agent but its schedule is missing or invalid.", "Set a valid cron expression (e.g. \"0 7 * * *\" for 7am daily).")
		} else {
			pass("schedule", "The schedule \""+cronOf(draft.Trigger)+"\" is a valid cron expression.")
		}
	}

	// Channel availability (Stories #11/#12): every channel the workflow
	// delivers to must be configured + enabled, else the agent can't deliver.
	channelsOK := true
	for _, ch := range draft.Channels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		if in.ChannelsConfigured != nil && !in.ChannelsConfigured[strings.ToLower(ch)] {
			channelsOK = false
			record(PreflightIssue{
				Severity: "block", Kind: "channel",
				Message:      "Delivers to the \"" + ch + "\" channel, which is not configured.",
				Fix:          "Configure and enable \"" + ch + "\" (add its token) in Channels settings.",
				Action:       "open_delivery",
				ActionParams: map[string]string{"channel": ch},
			})
		}
	}
	if channelsOK && in.ChannelsConfigured != nil && len(draft.Channels) > 0 {
		pass("channel", "Every delivery channel this agent uses ("+strings.Join(nonEmptyStrings(draft.Channels), ", ")+") is configured and enabled.")
	}

	// MCP connectivity verdict — the blockers are raised per node above; this
	// records the Ready tier so a client can show "MCP: connected" instead of
	// silence, which reads identically to "never checked".
	if servers := draftMCPServers(draft); len(servers) > 0 && in.ConnectedMCP != nil {
		if allMCPConnected(in.ConnectedMCP, servers) {
			pass("mcp", "Every MCP server this agent calls ("+strings.Join(servers, ", ")+") is connected.")
		}
	}

	// Credentials (ST-07): PreflightInput.SecretsSet was collected and never
	// read, so readiness reported green with every API key missing — the agent
	// then failed on its first real run, unattended. Evaluate it here.
	checkCredentials(draft, in, record, pass)

	// Runtime provider/model (ST-08): nothing verified that the AGENT's own
	// provider/model is configured and servable. (handleStudioModelAdvice judges
	// the BUILDER model — a different question with the same shape, which is
	// exactly why the gap went unnoticed.)
	checkRuntimeModel(draft, in, record, pass)

	// Template syntax: catch malformed {{ … }} expressions (e.g. {{ ..x }}) at
	// save time instead of at run time.
	checkTemplates(draft, add)

	// Output-variable contracts: every published flow var must be unique,
	// referenceable, and not collide with runtime-provided values.
	checkOutputContracts(draft, add)

	// Cross-step data-flow dependencies (Story #3): referenced flow vars must be
	// produced by an earlier step.
	checkDataFlow(draft, add)

	// Template-reference shape: a step that interpolates a whole produced object
	// ({{ .notebook }}) or a wrong nested path ({{ .notebook.notebook }}) renders
	// a Go map ("map[id:… title:…]") instead of the scalar it needs, silently
	// corrupting downstream tool inputs. Catch it at save instead of at run.
	checkTemplateReferences(draft, add)

	// Deep tool introspection (Architect): validate each tool call against the
	// tool's real signature — unknown argument names + literal type mismatches —
	// so a call that would silently fail at the MCP boundary is caught at build
	// time and fed to the repair loop.
	checkToolArgs(draft, in.Catalog, add)

	// Platform workflow policies: steer generated workflows toward safe Soulacy
	// primitives when the intent is KB ingestion or transient state. This catches
	// the common brittle pattern of using write_file where kb_write or queue_*
	// should be used, before a live run hits system authorization.
	checkPlatformWorkflowPolicies(draft, add)

	// Unattended-execution / confirmation tradeoff (Story #14): a scheduled agent
	// runs with no human present, so any step that acts on the system or network
	// executes WITHOUT approval. Warn and explain the tradeoff.
	checkUnattended(draft, add)

	res.OK = len(res.Blockers) == 0
	return res
}

// privilegedFlowTools are the built-in tools that act on the host/OS — the
// SEC-3 SYSTEM partition. A scheduled agent using one runs it unattended.
var privilegedFlowTools = map[string]bool{
	"shell_exec":      true,
	"run_script":      true,
	"install_library": true,
	"package_install": true,
	"write_file":      true,
	"delete_file":     true,
}

// checkUnattended warns when a SCHEDULED agent contains steps that act on the
// system or network (privileged built-in tools, or python nodes classified as
// needing system/network capabilities). These run with no one to approve them,
// so the user should consciously accept that — or add confirmation, which would
// instead pause each scheduled run waiting for approval. We surface the tradeoff
// rather than block. Story #14.
func checkUnattended(draft Draft, add func(sev, kind, node, msg, fix string)) {
	if !strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "schedule") {
		return
	}
	var sensitive []string
	for _, n := range draft.Flow.Nodes {
		if privilegedFlowTools[strings.TrimSpace(n.Tool)] {
			sensitive = append(sensitive, n.ID)
			continue
		}
		// python node needing system/network (classifyFlowNodes set Requires).
		for _, cap := range n.Requires {
			if cap == "system" || cap == "network" {
				sensitive = append(sensitive, n.ID)
				break
			}
		}
	}
	if len(sensitive) == 0 {
		return
	}
	if draft.Unattended {
		// Opted in: explain what that means rather than warning about a failure.
		add("warn", "confirmation", strings.Join(sensitive, ", "),
			"Unattended mode is ON: this scheduled agent's system/network step(s) will run automatically without asking for approval.",
			"Leave Unattended on for hands-off automation; turn it off if you'd rather these steps be approved manually (note: scheduled runs then can't complete those steps).")
		return
	}
	add("warn", "confirmation", strings.Join(sensitive, ", "),
		"This scheduled agent includes step(s) that act on your system or the network. A scheduled run has nobody to approve them, so they will be DENIED and the run will fail.",
		"Turn on Unattended mode to let these steps run automatically (only if you trust them), or run this agent manually instead of on a schedule.")
}

// runEntrypointRe matches a top-level (column-0) `def run(` definition — the
// contract the inline-Python harness calls. Indented defs (nested in another
// function/class) don't count: the harness's module-level run(args) call can't
// see them, which is exactly the "name 'run' is not defined" failure.
var runEntrypointRe = regexp.MustCompile(`(?m)^def\s+run\s*\(`)

// definesRunEntrypoint reports whether inline code defines a usable top-level
// run() function. Empty code is treated as missing.
func definesRunEntrypoint(code string) bool {
	return runEntrypointRe.MatchString(code)
}

// requiredParams parses a compact MCP param hint ("title*:string, summary:string")
// and returns the names marked required (with a trailing * on the name).
func requiredParams(hint string) []string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(hint, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name := p
		if i := strings.IndexByte(p, ':'); i >= 0 {
			name = strings.TrimSpace(p[:i])
		}
		if strings.HasSuffix(name, "*") {
			out = append(out, strings.TrimSpace(strings.TrimSuffix(name, "*")))
		}
	}
	return out
}

// paramTypes parses a compact MCP param hint ("title*:string, count:number")
// into arg name -> declared type (lowercased; "" when a param lists no type).
// The trailing required-marker "*" is stripped from names.
func paramTypes(hint string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(hint, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name, typ := p, ""
		if i := strings.IndexByte(p, ':'); i >= 0 {
			name = strings.TrimSpace(p[:i])
			typ = strings.TrimSpace(p[i+1:])
		}
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "*"))
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(typ)
	}
	return out
}

// nodeSuppliesViaPort reports whether a declared input port binds the given
// argument key — i.e. the value arrives by typed-port wire rather than in the
// node's Input. The bind key is the port's Field override when set, else its Name
// (matching the runtime's resolvePortInputs).
func nodeSuppliesViaPort(flow Flow, n sdkr.FlowNode, arg string) bool {
	for _, p := range n.Inputs {
		key := p.Name
		if p.Field != "" {
			key = p.Field
		}
		if key == arg {
			for _, e := range flow.Edges {
				if e.To == n.ID && e.ToPort == p.Name {
					return true
				}
			}
		}
	}
	return false
}

// builtinArgFilled reports whether a built-in tool's required argument is
// satisfied, including runtime-supported aliases. Studio teaches canonical
// names, but the engine intentionally accepts common variants so ReAct agents
// do not waste turns recovering from harmless field-name drift.
func builtinArgFilled(flow Flow, n sdkr.FlowNode, arg string) bool {
	tool := strings.TrimSpace(n.Tool)
	if tool == "channel.send" && arg == "text" {
		for _, k := range []string{"text", "message", "msg", "body", "content"} {
			if argFilled(n.Input, k) || nodeSuppliesViaPort(flow, n, k) {
				return true
			}
		}
		return false
	}
	return argFilled(n.Input, arg) || nodeSuppliesViaPort(flow, n, arg)
}

// argFilled reports whether the node's Input (a JSON object template) contains a
// real, non-placeholder value for the given argument name. A template
// expression ({{ ... }}) counts as filled — it will be rendered from an upstream
// output at run time. Empty strings and obvious placeholders do not count.
func argFilled(input, arg string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	// Try to parse as a JSON object first.
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err == nil {
		v, ok := m[arg]
		if !ok {
			return false
		}
		return !isPlaceholderValue(v)
	}
	// Not strict JSON (may contain {{ templates }}). Fall back to a textual
	// check: the key appears AND there's a template expr or a non-empty literal
	// somewhere near it. Conservative: require the key token to be present.
	if !strings.Contains(input, "\""+arg+"\"") {
		return false
	}
	// If a template expression is present anywhere, assume the value is wired.
	if strings.Contains(input, "{{") {
		return true
	}
	// Otherwise look for an obviously empty/placeholder assignment.
	return !hasEmptyAssignment(input, arg)
}

// isPlaceholderValue reports whether a parsed JSON value is empty or an obvious
// placeholder ("", "<no value>", "<notebook_id>", "TODO", null, etc.).
func isPlaceholderValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return true
		}
		ls := strings.ToLower(s)
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			return true
		}
		switch ls {
		case "todo", "tbd", "none", "no value", "<no value>", "null", "placeholder":
			return true
		}
		return false
	default:
		return false
	}
}

// hasEmptyAssignment looks for "arg": "" or "arg": <placeholder> in a non-JSON
// input string.
func hasEmptyAssignment(input, arg string) bool {
	key := "\"" + arg + "\""
	i := strings.Index(input, key)
	if i < 0 {
		return false
	}
	rest := strings.TrimSpace(input[i+len(key):])
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	return strings.HasPrefix(rest, "\"\"") || strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, "null")
}

// mcpServerOf extracts the server name from a full MCP tool name
// mcp__<server>__<tool>.
func mcpServerOf(full string) string {
	rest := strings.TrimPrefix(full, "mcp__")
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[:i]
	}
	return ""
}

// mcpConnected reports whether the named server is connected. A nil map means
// "unknown" — we do not block in that case (the caller didn't supply state).
func mcpConnected(m map[string]bool, server string) bool {
	if m == nil {
		return true
	}
	return m[server]
}

// hasNewAgent reports whether the draft defines the given agent id in NewAgents.
func hasNewAgent(draft Draft, id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, na := range draft.NewAgents {
		if strings.ToLower(strings.TrimSpace(na.ID)) == id {
			return true
		}
	}
	return false
}

// cronOf returns the cron expression from a schedule trigger's config, if any.
func cronOf(t Trigger) string {
	if t.Config == nil {
		return ""
	}
	if v, ok := t.Config["cron"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// validCron does a light structural check: a standard cron has 5 or 6
// whitespace-separated fields. This is intentionally lenient (the scheduler does
// the authoritative parse); it just catches the common "missing/garbage
// schedule" case at save time.
func validCron(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	if strings.HasPrefix(expr, "@") { // @daily, @hourly, @every 1h, ...
		return true
	}
	n := len(strings.Fields(expr))
	return n == 5 || n == 6
}

// lowerSet builds a case-insensitive membership set from a string slice.
func lowerSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			m[strings.ToLower(t)] = true
		}
	}
	return m
}
