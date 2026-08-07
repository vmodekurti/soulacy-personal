// Package studio implements the Studio plugin's backend: the intent
// compiler (Story S1.1). It turns a plain-language intent into a draft
// workflow plus clarifying questions — a hybrid that always returns a
// best-effort draft AND the questions needed to firm it up, never blocking.
//
// The compiler is deliberately split into small, independently testable
// functions (BuildPrompt → LLM.Complete → ParseDraft → validate → derive
// questions → notes) and depends only on a narrow LLM interface so it can
// be unit-tested with a fake model.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/agentprompt"
	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio/codeclass"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// LLM is the narrow completion seam the compiler depends on. Production
// wiring adapts the gateway's llm.Router to this; tests supply a fake.
type LLM interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// Catalog is the optional context the caller can supply so the model
// grounds the draft in real agents/tools/providers instead of inventing
// names. All fields are optional.
type Catalog struct {
	Agents    []string           `json:"agents,omitempty"`
	Tools     []string           `json:"tools,omitempty"`
	Providers []string           `json:"providers,omitempty"`
	Skills    []CatalogSkill     `json:"skills,omitempty"`
	MCP       []CatalogMCPServer `json:"mcp,omitempty"`
	// Channels are the configured output channels available to the workflow
	// (e.g. "telegram", "slack", "discord", "http"). Studio wires a workflow's
	// delivery to one of these instead of inventing a channel name.
	Channels []string `json:"channels,omitempty"`
	// RawIntent is the user's ORIGINAL words, before Studio's own refinement.
	//
	// Capability matching must use this, not the refined intent. The refiner
	// expands a one-line request into a long, domain-heavy specification that
	// shares ordinary vocabulary with dozens of unrelated skills, so matching
	// against it injects them all — the "~30 skills of bloat" failure that
	// groundSkills documents and guards against by preferring Draft.RawIntent.
	//
	// That guard was inert: Draft.RawIntent is populated only when LOADING a
	// saved agent, never during generation, so every generated draft fell back to
	// the refined text the guard exists to avoid. Carrying the original words
	// through the catalogue — which already carries the other generation context,
	// Rules and ReferenceGraph — is what makes it work.
	RawIntent string `json:"raw_intent,omitempty"`
	// MustUseTools / MustUseSkills name capabilities the graph is REQUIRED to
	// use, because the user named them in the prompt and this workspace has them.
	//
	// Set only on a coverage retry — the first attempt is given the catalogue and
	// left to choose. When a model has already listed a travel MCP server among
	// its options and still reached for web_search, restating the same catalogue
	// is unlikely to produce a different answer; naming the omission is.
	MustUseTools  []string `json:"must_use_tools,omitempty"`
	MustUseSkills []string `json:"must_use_skills,omitempty"`
	// StructureCorrection is the same idea for SHAPE rather than capability: set
	// on a structure retry, when the first graph collapsed a described fan-out
	// into one step. Rendered verbatim ahead of the catalogue for the same
	// reason MustUse is — it frames how the rest of the brief is read.
	StructureCorrection string `json:"structure_correction,omitempty"`
	// KnowledgeBases are the knowledge bases the agent could draw on, so Studio
	// can attach a relevant KB instead of starting from scratch (Story #7).
	KnowledgeBases []CatalogKB `json:"knowledge_bases,omitempty"`
	// Rules is the user-editable SOUL.yaml authoring rulebook. When set, the
	// builder injects it so generation follows the same rules validation and the
	// AI fixer enforce. Not part of the live inventory — it's guidance.
	Rules string `json:"rules,omitempty"`
	// Lessons are guidance sentences distilled from accepted live-run repairs
	// (real API shapes that broke a node and the fix that worked). Injected into
	// the generation prompt so Studio learns to build flows that work the first
	// time. Populated server-side from the lesson store; not user-authored.
	Lessons []Lesson `json:"lessons,omitempty"`
	// Generation is the server-derived build profile for the Studio builder
	// model. It lets the compiler tighten prompts for compact local models and
	// report how much scaffolding was used, without relying on the GUI to infer
	// model quality or locality.
	Generation *GenerationProfile `json:"generation,omitempty"`
	// ReferenceGraph is a known-good graph for this KIND of job, offered to the
	// model as a worked example rather than used verbatim.
	//
	// It exists because the two ways of building a graph each fail differently.
	// The curated planners encode ordering knowledge a builder model does not
	// have — the NotebookLM podcast flow wires create → add sources → generate →
	// poll in the one sequence that works, and asking a model to invent it
	// produced a useless single-node web_search graph. But those same planners
	// are blind to the rest of an installation: they never reference a skill and
	// only reference the MCP servers someone hard-coded a case for.
	//
	// Showing the curated graph to the model gets both: the model still designs,
	// still reads the whole catalogue, and still adapts to what the user actually
	// asked for — but it starts from a demonstrated-correct shape instead of
	// guessing one. Empty for intents no curated pattern covers.
	ReferenceGraph string `json:"reference_graph,omitempty"`
}

// CatalogKB is one knowledge base the workflow's agents could use as a source.
// Name is the exact KB name; Description lets the compiler decide relevance.
type CatalogKB struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Documents   int    `json:"documents,omitempty"`
}

// CatalogMCPServer is one connected MCP server and the tools it exposes, so the
// model can wire MCP tools when the intent names a server or a capability it
// provides ("use the github mcp", "create a notebook").
type CatalogMCPServer struct {
	Server string           `json:"server"`
	Tools  []CatalogMCPTool `json:"tools,omitempty"`
}

// CatalogMCPTool is one callable MCP tool. Name is the EXACT value to put in a
// tool node's "tool" field; Description lets the model map a loose reference to
// the right tool.
type CatalogMCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Params is a compact argument hint (e.g. "title*:string, summary:string",
	// required marked with *) so the model passes the tool's REAL keyword
	// arguments instead of inventing names like "name".
	Params string `json:"params,omitempty"`
}

// CatalogSkill is an installed Agent Skill the model may reference via a
// read_skill node. Name is the EXACT skill name to put in skill_name; the
// Description lets the model map a loose reference in the intent ("yahoo
// finance", "stock data") to the right installed skill ("yfinance").
type CatalogSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Request is the POST /api/v1/studio/compile body.
type Request struct {
	Intent  string  `json:"intent"`
	Catalog Catalog `json:"catalog,omitempty"`
	// RawIntent is the user's ORIGINAL prompt before refinement (Intent is the
	// refined version). The server's architecture decision (RecommendAgentMode) is
	// evaluated over BOTH so a strong cue present only in the raw text — and lost
	// during refinement — still routes correctly, matching what refine decided.
	RawIntent string `json:"raw_intent,omitempty"`
	// Answers carries the user's responses to clarifying questions from a
	// prior compile (question id -> answer). When present they are woven
	// into the prompt so a re-compile incorporates them, closing the
	// clarify round-trip. Optional.
	Answers map[string]string `json:"answers,omitempty"`
	// ForceWorkflow makes /compile build a fixed workflow graph and skip the
	// server-side RecommendAgentMode routing. Set when the user EXPLICITLY picks
	// the Workflow mode toggle — an explicit human choice overrides the
	// reasoning-fit heuristic, which otherwise reverts their pick to an agent.
	ForceWorkflow bool `json:"force_workflow,omitempty"`
}

// Trigger describes how the workflow starts.
type Trigger struct {
	Type   string         `json:"type"`             // schedule | channel | webhook | manual
	Config map[string]any `json:"config,omitempty"` // e.g. {"cron": "0 8 * * 1-5"}
}

// Flow is the graph form, mirroring the sdk/reasoning JSON shapes so the
// draft round-trips straight into reasoning.CompileFlow.
type Flow struct {
	Nodes  []sdkr.FlowNode `json:"nodes"`
	Edges  []sdkr.FlowEdge `json:"edges,omitempty"`
	Entry  string          `json:"entry,omitempty"`
	Output string          `json:"output,omitempty"` // node id whose result is the flow's output (default: last node)
	// MaxNodeExecutions caps total node executions for a workflow run (0 = engine
	// default). Preserved across the canvas round-trip so a user-set budget isn't
	// silently reset on re-save.
	MaxNodeExecutions int `json:"max_node_executions,omitempty"`
}

// NewAgent defines a full profile for an agent the model invented.
type NewAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
}

// Recommendation is the compiler's assessment of which execution model best
// fits the intent. It's advisory: the compiler still emits a workflow graph so
// the editor has something to show, but the recommendation tells the user when
// a reasoning strategy (react/plan_execute) would serve the task better than a
// frozen graph — e.g. when steps depend on values only known at run time.
type Recommendation struct {
	Mode      string `json:"mode"`      // workflow | react | plan_execute
	Rationale string `json:"rationale"` // 1–2 sentences explaining the pick
}

// Draft is the workflow the compiler produces.
type Draft struct {
	// ID is the existing agent's id when a saved agent was opened for editing.
	// Empty for a brand-new draft (the id is then derived from Name on save).
	// Carrying it makes Save target the SAME agent instead of creating a new one
	// when the name doesn't slug back to the original id (e.g. after a rename).
	ID           string `json:"id,omitempty"`
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Intent       string `json:"intent,omitempty"` // the prompt that generated this workflow (Studio editor)
	// Refined marks that Intent has already been through a full refine pass and
	// (optionally) hand-edited by the user. The UI sets it after the first
	// generate; it persists via Definition.StudioRefined so a re-opened workflow
	// re-generates with a fast LIGHT touch-up instead of a full re-refine.
	Refined bool `json:"refined,omitempty"`
	// RawIntent is the user's ORIGINAL prompt before refinement (Intent holds the
	// refined version). Persisted via Definition.StudioRawIntent so the prompt
	// editor can show and re-refine the original.
	RawIntent      string          `json:"raw_intent,omitempty"`
	Trigger        Trigger         `json:"trigger"`
	Channels       []string        `json:"channels,omitempty"`
	Output         *ScheduleOutput `json:"output,omitempty"`
	Flow           Flow            `json:"flow"`
	NewAgents      []NewAgent      `json:"new_agents,omitempty"`
	Recommendation *Recommendation `json:"recommendation,omitempty"`
	// Knowledge lists knowledge base names to attach to the agent so it can draw
	// on existing documents (Story #7). The model may set this from the Available
	// knowledge bases when the subject matches; mapped to Definition.Knowledge on
	// save.
	Knowledge []string `json:"knowledge,omitempty"`
	// Unattended opts the saved agent into auto-approving guardrail confirmations
	// in non-interactive (e.g. scheduled) runs (Story #14). Set by the user via
	// the Studio toggle; mapped to Definition.Unattended on save.
	Unattended bool `json:"unattended,omitempty"`

	// Outcome is the draft's BUSINESS-OUTCOME CONTRACT (P0-4): what a run must
	// actually achieve. Assertions used to live only in a test run and were
	// discarded at save, so a saved agent was judged in production by "did a
	// node error" alone. Mapped to Definition.Outcome on save, where the runtime
	// re-evaluates it against real runs.
	Outcome *OutcomeSpec `json:"outcome,omitempty"`

	// ── ReAct / Plan-Execute agent form (local-first pivot) ───────────────────
	// When Strategy is set ("react" or "plan_execute"), this Draft is NOT a fixed
	// workflow — it's a reasoning agent. The engine runs the strategy loop and
	// the Flow is left empty (a workflow block would override the strategy). The
	// agent drives Tools (an allowlist of builtin + mcp__ tool names) and Skills
	// dynamically, guided by SystemPrompt, with NewAgents as peers and Knowledge
	// attached. Used for tasks that loop or depend on intermediate results
	// (e.g. NotebookLM: add each source, poll audio status until ready).
	Strategy string   `json:"strategy,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	Skills   []string `json:"skills,omitempty"`
	// Policy carries the per-strategy contract the Studio Build step edits: the
	// agent's goal and completion criteria, the ReAct loop's stop/recovery rules,
	// and the Plan-Execute planner's steps and approval gates.
	//
	// These were previously implicit — a reasoning agent's stop condition lived
	// only in prose inside SystemPrompt, so it could not be shown, validated, or
	// enforced. Nil means "use Studio's defaults for the selected strategy",
	// which is what every existing draft gets.
	Policy *AgentPolicy `json:"policy,omitempty"`
	// Reasoning-loop budgets for an agent draft. Preserved across the canvas/YAML
	// round-trip so a user who tuned them in SOUL.yaml doesn't have them reset to
	// defaults on re-save. Empty/zero means "use Studio's sensible default".
	StepTimeout  string `json:"step_timeout,omitempty"`
	TotalTimeout string `json:"total_timeout,omitempty"`
	MaxSteps     int    `json:"max_steps,omitempty"`
	MaxPlanSteps int    `json:"max_plan_steps,omitempty"`
	MaxTurns     int    `json:"max_turns,omitempty"`
	// RunTimeout is the whole-run wall-clock cap (top-level agent field, distinct
	// from the reasoning step/total budgets). Carried so it survives a Studio
	// round-trip — without it the code view re-rendered SOUL.yaml without the
	// run_timeout the user had set on disk.
	RunTimeout string `json:"run_timeout,omitempty"`
	// LLM carries the agent's provider/model/temperature/etc. so they survive a
	// Studio round-trip. Without this, FromAgentDefinition dropped the block and
	// ToAgentDefinition re-emitted a hard-coded default, silently clobbering a
	// provider/model the user set on the Agents screen or directly in SOUL.yaml.
	LLM agent.LLMConfig `json:"llm,omitempty"`

	// ConfirmTools and Security are generated alongside the architecture so
	// Studio does not leave production guardrails as after-the-fact warnings.
	// They map directly to agent.Definition on save and round-trip through
	// Studio edits.
	ConfirmTools []string              `json:"confirm_tools,omitempty"`
	Security     *agent.SecurityConfig `json:"security,omitempty"`
}

// ScheduleOutput describes the scheduled delivery target for generated agents.
// It mirrors pkg/agent.ScheduleOutput but stays in the Studio package so draft
// JSON can round-trip through the GUI before becoming a SOUL.yaml definition.
type ScheduleOutput struct {
	Channel  string `json:"channel,omitempty"`
	To       string `json:"to,omitempty"`
	BotName  string `json:"bot_name,omitempty"`
	Template string `json:"template,omitempty"`
}

// IsAgent reports whether the draft is a reasoning/tool agent (no fixed flow
// graph). "auto" is the recommended default — it produces a tool agent whose
// execution mode the engine resolves at run time (classic native-tool-calling
// for capable models, ReAct otherwise). "react"/"plan_execute" pin an explicit
// strategy.
func (d Draft) IsAgent() bool {
	return isAgentStrategy(d.Strategy)
}

// isAgentStrategy reports whether a strategy string denotes a tool agent (as
// opposed to a fixed workflow). Centralised so every surface agrees.
func isAgentStrategy(strategy string) bool {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "auto", "react", "plan_execute":
		return true
	default:
		return false
	}
}

// Question is one clarifying question. Options, when present, suggest a
// closed set of answers the UI can render as choices.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

// Suggestion flags a capability a draft REFERENCES but that is not present
// in the provided Catalog, so the UI can offer to install/discover it
// (Stories S4.1/S4.3). Kind is one of tool|agent|skill|mcp; today the
// compiler emits tool and agent suggestions (the only capability kinds a
// flow node can reference). Installed reports whether the capability is in
// the catalog: suggestMissing returns only Installed:false entries (the
// actionable set), but the kind is part of the contract so the UI can route
// "install tool" vs "discover agent" appropriately.
type Suggestion struct {
	Kind      string `json:"kind"`   // tool | agent | skill | mcp
	Name      string `json:"name"`   // the referenced capability's name/id
	Reason    string `json:"reason"` // human-readable why it's suggested
	Installed bool   `json:"installed"`
}

// Result is the compile response: a draft, clarifying questions, transparency
// notes about what was inferred and why, and suggestions for capabilities the
// draft references but that aren't in the caller's catalog.
type Result struct {
	Workflow    Draft        `json:"workflow"`
	Questions   []Question   `json:"questions"`
	Notes       []string     `json:"notes"`
	Suggestions []Suggestion `json:"suggestions"`
	// Explanation is a plain-language description of what was built (Story #10):
	// purpose, steps, tools/channels, and the chosen architecture. Derived
	// deterministically from the draft.
	Explanation *DraftExplanation `json:"explanation,omitempty"`
	// Plan is the deterministic skeleton the model was asked to realise (local-
	// first pivot). Present when the intent matched a known pattern; surfaced for
	// transparency so the user sees the framework did the structural planning.
	Plan []PlanStep `json:"plan,omitempty"`
	// Generation describes the local/cloud builder profile and confidence guard
	// rails used for this compile. It is derived server-side from the actual
	// configured Studio provider/model and returned so the UI can explain why a
	// local model got stricter checks or why Studio recommends build verification.
	Generation *GenerationProfile `json:"generation,omitempty"`
	// Contract is the deterministic generation verdict for the returned draft.
	// It lets Studio surface blockers immediately after generation, before the
	// operator gets as far as Save or creates a broken agent on disk.
	Contract *ContractResult `json:"contract,omitempty"`
}

// canonicalExample is the shape the model is instructed to emit. It is
// embedded verbatim in the prompt so the model has a concrete target. It is
// deliberately a RICH, MULTI-NODE graph (Story M3): multiple tool AND agent
// (peer) nodes, a branch node fanning out conditional edges, and typed ports
// wired with from_port/to_port — so the model treats branching, peer-agent
// handoff, and named ports as the norm, not the exception.
const canonicalExample = `{
  "name": "Weekday AI Digest",
  "system_prompt": "You are a specialized AI news curator. You wake up every weekday morning to compile a brief, high-signal digest of the latest artificial intelligence research and product news. You execute your workflow methodically: searching the web, filtering out noise with a python script, and delegating the final synthesis to your summarizer agent. Your tone is professional and concise. When encountering empty search results or errors, you gracefully emit a fallback message rather than failing. Stick strictly to the defined workflow steps.",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["<channel ids the user named, from the installed list above; omit entirely if they named none>"],
  "flow": {
    "nodes": [
      { "id": "search_ai_news", "kind": "tool", "tool": "web_search",
        "description": "Search the web for today's top AI news",
        "input": "{\"query\": \"latest artificial intelligence research and product news\", \"num_results\": 10}",
        "output": "search", "x": 0, "y": 0 },
      { "id": "pick_top_articles", "kind": "python",
        "description": "Parse the search results and keep the top 5 as {title,url}",
        "code": "def run(inputs):\n    try:\n        res = inputs.get('search', {})\n        if not isinstance(res, dict):\n            res = {}\n        items = res.get('results') or res.get('items') or []\n        return [{'title': it.get('title', ''), 'url': it.get('url') or it.get('link', '')} for it in items[:5] if isinstance(it, dict)]\n    except Exception as e:\n        return []",
        "output": "articles", "x": 220, "y": 0 },
      { "id": "have_articles", "kind": "branch",
        "description": "Continue only if at least one article was found",
        "inputs":  [ { "name": "in" } ],
        "outputs": [ { "name": "hot" }, { "name": "quiet" } ], "x": 440, "y": 0 },
      { "id": "write_digest", "kind": "agent", "agent": "summarizer",
        "description": "Summarize the articles into a 5-bullet digest",
        "input": "You are an expert AI news curator. Your task is to write a concise, 5-bullet AI digest based on the provided articles. For each article, write a single clear sentence summarizing the takeaway, followed immediately by the URL. Do not include any introductory text or fluff. Output strictly as a markdown bulleted list.\n\nSource Articles:\n{{ toJson .articles }}",
        "output": "digest", "x": 660, "y": -80 },
      { "id": "say_quiet", "kind": "agent", "agent": "notifier",
        "description": "Send a short 'nothing notable today' note",
        "input": "Reply exactly with this fallback message: No notable AI news today. Enjoy the quiet!",
        "output": "note", "x": 660, "y": 80 }
    ],
    "edges": [
      { "from": "search_ai_news", "to": "pick_top_articles" },
      { "from": "pick_top_articles", "to": "have_articles" },
      { "from": "have_articles", "to": "write_digest", "from_port": "hot", "if": "{{ gt (len .articles) 0 }}" },
      { "from": "have_articles", "to": "say_quiet", "from_port": "quiet" },
      { "from": "write_digest", "to": "end" },
      { "from": "say_quiet", "to": "end" }
    ],
    "entry": "search_ai_news"
  },
  "new_agents": [
    {
      "id": "summarizer",
      "name": "Summarizer",
      "description": "Summarize articles into a 5-bullet digest",
      "system_prompt": "You are an expert AI news curator. Your task is to write a concise, 5-bullet AI digest based on the provided articles. For each article, write a single clear sentence summarizing the takeaway, followed immediately by the URL. Do not include any introductory text or fluff. Output strictly as a markdown bulleted list."
    },
    {
      "id": "notifier",
      "name": "Notifier",
      "description": "Output static notifications",
      "system_prompt": "You are a simple notifier. You output the exact message provided to you without any formatting or conversational text."
    }
  ]
}`

// writeMustUseBlock renders the coverage CORRECTION, shared by the workflow and
// agent builder prompts.
//
// Shared rather than written into each, because the first version of this lived
// only in the workflow prompt. The retry still FIRED for agents — it just sent
// an unchanged prompt and so could never close the gap, which is worse than not
// retrying at all: it burns a model call and then reports "retry did not help",
// implying the model refused when in fact it was never told anything new. A
// capability the user named is missing in exactly the same way whichever
// architecture is being built, so the correction belongs in one place.
//
// `graph` selects the wording: a workflow is corrected by adding a tool NODE, an
// agent by adding to its tool allowlist.
func writeMustUseBlock(sb *strings.Builder, catalog Catalog, graph bool) {
	// Structure corrections ride in the same slot: both are "your last attempt
	// missed something specific", and both must be read before the catalogue.
	if c := strings.TrimSpace(catalog.StructureCorrection); c != "" {
		sb.WriteString(c)
	}
	if len(catalog.MustUseTools) == 0 && len(catalog.MustUseSkills) == 0 {
		return
	}
	// Before the catalogue listing on purpose: it frames how that list is read.
	// The previous attempt failed by treating the catalogue as a menu of options
	// rather than a set of commitments.
	sb.WriteString("CORRECTION — YOUR PREVIOUS ATTEMPT IGNORED A CAPABILITY THE USER NAMED.\n")
	sb.WriteString("The user's request explicitly refers to the following, and this workspace has them installed.\n")
	if len(catalog.MustUseTools) > 0 {
		if graph {
			sb.WriteString("You MUST emit a `tool` node calling EACH of these, using its exact name and its real argument names:\n")
		} else {
			sb.WriteString("EACH of these MUST appear in the agent's \"tools\" allowlist by its exact name, and the system_prompt MUST say when to call it:\n")
		}
		for _, t := range catalog.MustUseTools {
			sb.WriteString("  - " + t + "\n")
		}
		sb.WriteString("Do NOT substitute web_search, fetch_url, http_request, or a python node for any of them. ")
		sb.WriteString("A generic web search is NOT an acceptable stand-in for a purpose-built tool the user asked for: it returns pages about the subject instead of the data the tool returns.\n")
	}
	if len(catalog.MustUseSkills) > 0 {
		sb.WriteString("You MUST include these skills:\n")
		for _, s := range catalog.MustUseSkills {
			sb.WriteString("  - " + s + "\n")
		}
	}
	sb.WriteString("Everything else is still your judgement — only the omission above is being corrected.\n\n")
}

// BuildPrompt builds the instruction the model must answer. It pins the
// canonical Draft JSON shape and demands JSON-only output, optionally
// grounding the model in the supplied catalog and weaving in any answers
// the user gave to clarifying questions from a prior compile.
func BuildPrompt(intent string, catalog Catalog, answers map[string]string) string {
	// Keep only the grounding most relevant to the intent when the catalog is
	// large, so the prompt stays focused (and within reach of weaker models).
	// No-op for typical/small setups.
	catalog = FilterCatalogForIntent(intent, catalog)
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio intent compiler. ")
	sb.WriteString("Turn the user's plain-language intent into a draft automation workflow.\n\n")
	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object. No prose, no markdown, no code fences.\n")
	sb.WriteString("- The JSON MUST match this exact schema (field names and nesting):\n\n")
	sb.WriteString(canonicalExample)
	sb.WriteString("\n\n")
	sb.WriteString("Schema notes:\n")
	sb.WriteString("- trigger.type is one of: schedule, channel, webhook, manual.\n")
	sb.WriteString("- For schedule triggers, put a cron expression in trigger.config.cron.\n")
	sb.WriteString("- channels is a list of output channel names (e.g. \"telegram\", \"slack\", \"email\").\n")
	sb.WriteString("- THE OUTPUT NODE IS THE ANSWER, NOT A DELIVERY RECEIPT: the flow's output node (its result is the reply the user reads AND what is delivered) MUST be the node that produces the human-readable CONTENT — the agent/llm/python node that formats the final message, summary, chart URL, or answer. NEVER make a `channel.send` (or any pure delivery/notify) node the terminal/output node: `channel.send` returns a delivery receipt like {\"ok\":true,\"channel\":...}, which is a useless reply. Set the content node as the last node (or reference it via the flow-level output field). Deliver to channels via the `channels` list, not by ending the graph on channel.send.\n")
	sb.WriteString("- channel.send IS FOR OUT-OF-BAND DELIVERY ONLY (e.g. pushing a scheduled result to Telegram/Slack). For channel/manual/webhook (interactive) triggers the reply is returned to the caller automatically — do NOT route the answer through channel.send. If a run must BOTH answer interactively AND push to a channel, produce the content in a node, make THAT node the output, and add channel.send as a SEPARATE branch — never as the output node.\n")
	// Parallel is taught here as well as allowed by the schema. The enum alone
	// only stops the model being REJECTED for emitting one; it does not tell the
	// model the kind exists, and a model that has never seen it will keep writing
	// independent fetches as a serial chain.
	sb.WriteString("- USE `parallel` FOR INDEPENDENT WORK: when two or more steps do not depend on each other's output (e.g. searching three different sources, or fetching several URLs), emit a node of kind \"parallel\" whose edges fan out to those steps, instead of chaining them one after another. Set \"join\" to one of: \"all\" (wait for every branch — the default), \"any\" (first success wins), \"quorum\" with \"join_quorum\": N (proceed once N succeed), or \"best_effort\" (continue with whatever succeeded). Use \"max_parallel\": N to cap concurrency when the branches hit the same rate-limited service. Serial chaining of genuinely independent steps is a correctness-neutral but user-visible slowdown, so prefer parallel whenever the steps do not read each other's outputs.\n")
	// Stated as a hard requirement, not a convenience. The line below already
	// mentioned {{ .trigger.text }}, but only as an alternative to inventing a
	// passthrough start node — so a model could read it, avoid the start node, and
	// still hardcode a query built from the build spec. The result runs, passes
	// every structural check, and answers the same thing on every run: asked for
	// the weather in a named town, one such graph returned a research digest on
	// how to build a weather bot, because that was the intent it was compiled
	// from and the user's message was read by nothing.
	sb.WriteString("- IF THE TRIGGER IS channel OR webhook, THE GRAPH MUST CONSUME THE INBOUND MESSAGE. The person's text arrives as {{ .trigger.text }} (aliases {{ .trigger.message }} / {{ .trigger.input }}) and MUST appear in the first node that searches, fetches, or prompts — e.g. \"input\": {\"query\":\"{{ .trigger.text }}\"}. Do NOT hardcode a query, topic, or prompt derived from this build description: the build description says what the workflow IS, the inbound message says what THIS RUN must answer. A channel-triggered graph that never references the trigger will return an identical result to every question it is ever asked, which is always a bug. (A schedule trigger has no inbound message; for those, a fixed query is correct.)\n")
	sb.WriteString("- VALID NODE KINDS ARE ONLY: tool, agent, python, llm, branch, parallel. Do NOT invent a \"start\", \"entry\", \"begin\", \"end\", or \"receive_request\" node — those kinds are invalid and break the flow. The graph has NO separate start/end node: the FIRST real node (usually an `llm` extractor or an `agent`) is the entry, named in the top-level `entry` field. The user's inbound text is available to any node as {{ .trigger.text }} (aliases {{ .trigger.message }} / {{ .trigger.input }}) — reference it directly instead of creating a passthrough start node.\n")
	sb.WriteString("- KEEP GRAPHS SIMPLE, but COMPOSE THE CAPABILITIES YOU HAVE: aim for a handful of meaningful nodes, not a 10-15 step pipeline. Collapse pure DATA GLUE (parsing, reshaping, dedupe, formatting) into a SINGLE `python` node. But do NOT collapse real OPERATIONS into python: when an available tool / MCP tool / skill performs the operation, emit a discrete `tool` node that CALLS it, and sequence several such nodes for a multi-step external job (e.g. create -> add sources -> generate -> poll). Delegate open-ended reasoning/summarizing to an `agent` node.\n")
	sb.WriteString("- USE `llm` NODES FOR FUZZY HUMAN LANGUAGE: when a downstream tool needs clean structured arguments (city, ticker, date range, product query, intent) but the trigger text may be phrased many ways, insert a `llm` node before the tool. Put the raw text in `input`, set params.system to an extraction instruction, set params.response_format to \"json\", and store the object in `output`. Then wire/pass only the extracted scalar fields to tools. Do not use brittle regex Python for natural-language intent extraction.\n")
	sb.WriteString("- PRODUCTION MINDSET: Treat every intent as a production workload. Handle empty states, edge cases, and failure modes explicitly (e.g. using a branch node to emit a fallback message if no items are found).\n")
	sb.WriteString("- COMPLETION CONTRACT: the workflow is not complete until every operation the user asked for has actually happened. If the intent says search/find PLUS create/generate/store/send/deliver, the graph must include the later operation(s); never stop at raw search JSON, IDs, delivery receipts, or intermediate tool output. The output node must be the final human-readable content or a clear fallback explaining which required operation failed.\n")
	sb.WriteString("- STANDARD DATA FORMAT — every handoff between steps is JSON, always. A step's output is a JSON value; the next step receives JSON. To pass a structured value (list/object) into a tool or python input, EITHER leave a python node's input EMPTY (it then auto-receives all upstream outputs as a JSON `inputs` dict) OR use {{ toJson .var }} UNQUOTED, e.g. \"urls\": {{ toJson .urls }}. NEVER write \"urls\": \"{{ .urls }}\" — Go renders a list/object that way as `[map[...] ...]` text (not JSON) and the next step breaks. A bare {{ .scalar }} is only for a single scalar value inside a string.\n")
	sb.WriteString("- system_prompt: Write a rich, conversational system prompt for the overarching agent (2-4 sentences). Give it a clear persona, define its goal based on the intent, and explicitly outline the multi-step strategy it must follow. Instruct it to gracefully emit a fallback message on errors rather than failing.\n")
	sb.WriteString("- SHARED SYSTEM PROMPT CONTRACT: ")
	sb.WriteString(agentprompt.InstructionForBuilders())
	sb.WriteString("\n")
	sb.WriteString("- AGENTS MUST BE FULLY DEFINED — never blank. If the workflow needs to delegate to a specialized peer, define the peer in the `new_agents` array.\n")
	sb.WriteString("- WRITE REUSABLE AGENT PERSONAS: a helper agent like a summarizer or notifier may be reused across many tasks, so its `system_prompt` must be a complete, standalone persona — NOT a one-liner. Include: (1) its role and expertise, (2) exactly how it should behave and reason, (3) the precise OUTPUT format it must produce, and (4) how to handle empty/erroneous input gracefully. Aim for 3-6 sentences. The overarching agent's `system_prompt` contains the workflow plan; the helper agent's `system_prompt` is its durable, scenario-independent character.\n")
	sb.WriteString("- HIGH-QUALITY SPECIALIST PROMPTS: when creating a domain expert agent, write a prompt that would be useful in production on day one. Include: role identity, decision objective, tool-selection rules, required output sections, confidence/uncertainty handling, safety/fallback behavior, and exact formatting/artifact instructions. For example, a weather expert should not merely report conditions; it should help the user decide what to do, choose current/forecast/alerts tools correctly, include best/risk windows, confidence, alerts, practical planning guidance, and chart blocks when time-series data exists.\n")
	sb.WriteString("- SKILLS: when the intent references a capability/data source by a loose name (e.g. \"yahoo finance\", \"stock data\", \"web research\"), do NOT invent a skill name. Look in the \"Available skills\" list below and MATCH the reference to the closest installed skill by its name, then add it to the `skills` array.\n")
	sb.WriteString("- PREFER TYPED CAPABILITIES OVER GUESSED CODE: the tools, skills, and MCP tools listed below come with their REAL argument names. When a step's operation is covered by one of them, ALWAYS emit a `tool` node that calls it with those EXACT named arguments — a typed contract that always works — one operation per node. NEVER re-implement a typed tool inside a `python` node, and NEVER shell out to a CLI (subprocess) for something an MCP/tool already exposes: the model would have to guess CLI flags, which is exactly what breaks. For a multi-step MCP job, emit the discrete tool nodes IN SEQUENCE and wire each step's output into the next via typed ports (or {{ toJson .var }}). Reach for a `python` node ONLY for glue no tool covers. Do NOT invent tool/MCP names; if truly nothing fits, then a python node.\n")
	sb.WriteString("- EXECUTION MODE: judge which execution model best fits the intent and include a top-level \"recommendation\": {\"mode\":\"workflow|auto|react|plan_execute\", \"rationale\":\"<1-2 sentences>\"}. Pick \"workflow\" when the task is a clear FIXED sequence of tool/MCP/agent nodes (python only for glue). Pick \"auto\" — the recommended default for a CONVERSATIONAL, TOOL-USING, or SCHEDULED DIGEST agent that decides which tools to call as it goes (e.g. a flight finder, a research assistant, a morning news/weather digest): the engine runs it as a reliable native tool-calling loop, no fixed graph. Pick \"plan_execute\" for open-ended or long-horizon work that must plan and adapt over many steps. Do NOT auto-pick \"react\"; use it only when the user explicitly asks for ReAct, a think-act-observe loop, or a manual ReAct experiment.\n\n")

	// A demonstrated-correct graph for this kind of job, when one exists. Placed
	// before the rulebook so the model reads the shape first, and framed as a
	// STARTING POINT — copying it verbatim would throw away the reason the model
	// is designing at all.
	if ref := strings.TrimSpace(catalog.ReferenceGraph); ref != "" {
		sb.WriteString("\nREFERENCE GRAPH — a known-good workflow for this KIND of job, built by Soulacy's curated planner:\n")
		sb.WriteString(ref)
		sb.WriteString("\nTreat it as a worked example, NOT a template to copy. Its step ORDER and its tool/MCP wiring are known to work — keep them where they apply. But you can see the whole installed catalogue and it cannot: if the user asked for a capability this reference does not use (an MCP server, a skill, a channel), wire that in, add or drop steps, and adapt the shape to what was actually asked for. Where the two disagree, the user's intent wins.\n\n")
	}

	// User-editable authoring rulebook (same rules the validator + AI fixer use),
	// so generation follows them up front instead of being corrected after.
	sb.WriteString(RulesPromptBlock(catalog.Rules))

	// Lessons distilled from accepted live-run repairs — real API shapes that
	// broke a node before, so this generation avoids repeating the same mistake.
	sb.WriteString(LessonsPromptBlock(catalog.Lessons))

	// Builder profile guidance is deliberately close to the top of the prompt:
	// compact local models follow a short, explicit contract much better than
	// long free-form instructions buried later in the context.
	sb.WriteString(GenerationProfilePromptBlock(catalog.Generation))

	if len(catalog.Skills) > 0 {
		sb.WriteString("Available skills (use the EXACT name in read_skill's skill_name):\n")
		for _, sk := range catalog.Skills {
			name := strings.TrimSpace(sk.Name)
			if name == "" {
				continue
			}
			desc := strings.TrimSpace(sk.Description)
			if len(desc) > 200 {
				desc = desc[:200] + "…"
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			if desc != "" {
				sb.WriteString(" — ")
				sb.WriteString(desc)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	writeMustUseBlock(&sb, catalog, true)

	if len(catalog.MCP) > 0 {
		sb.WriteString("Available MCP servers and their tools (use the EXACT tool name in a tool node):\n")
		for _, srv := range catalog.MCP {
			name := strings.TrimSpace(srv.Server)
			if name == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			if len(srv.Tools) == 0 {
				sb.WriteString(" (connected; no tools exposed)")
			}
			sb.WriteString("\n")
			for _, t := range srv.Tools {
				tn := strings.TrimSpace(t.Name)
				if tn == "" {
					continue
				}
				desc := strings.TrimSpace(t.Description)
				// MCP descriptions carry the usage/workflow hints the model needs to
				// pick the right tool + sequence (e.g. "add_source action", "poll
				// status until completed"), so keep them generous, not 1-line.
				if len(desc) > 400 {
					desc = desc[:400] + "…"
				}
				sb.WriteString("    • ")
				sb.WriteString(tn)
				if p := strings.TrimSpace(t.Params); p != "" {
					sb.WriteString("(")
					sb.WriteString(p)
					sb.WriteString(")")
				}
				if desc != "" {
					sb.WriteString(" — ")
					sb.WriteString(desc)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\nFor an MCP tool node, set \"input\" to a JSON object using ONLY the argument names shown in that tool's (parentheses) — required args are marked with *. Do NOT invent argument names (e.g. don't pass \"name\" when the tool lists \"title\").\n\n")
	}

	if len(catalog.Agents) > 0 {
		sb.WriteString("Available agents: ")
		sb.WriteString(strings.Join(catalog.Agents, ", "))
		sb.WriteString("\n")
		// Listing the agents is not the same as asking for them to be used. A
		// generated graph that invents "summarizer" when the workspace already
		// has one leaves the user with two near-identical agents and no idea
		// which one a run actually used.
		sb.WriteString("PREFER AN EXISTING AGENT: if one of the agents listed above already does the job a step needs, reference it by its EXACT id in that node's \"agent\" field instead of inventing a new peer. Only add a new entry to `new_agents` when nothing listed fits — every new peer becomes a real agent in the user's workspace on save.\n")
	}
	if len(catalog.Tools) > 0 {
		sb.WriteString("Available tools: ")
		sb.WriteString(strings.Join(catalog.Tools, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("Built-in tool contracts you MUST obey:\n")
	for _, name := range sortedBuiltinToolNames() {
		sb.WriteString("- ")
		sb.WriteString(name)
		sb.WriteString("(")
		sb.WriteString(toolParamPrompt(builtinToolParams()[name]))
		sb.WriteString(")\n")
	}
	sb.WriteString("- For intents like \"store in Soulacy's Knowledge store\", \"save to KB\", \"ingest documents/URLs into knowledge\", use a safe Knowledge Ingestion flow and the `kb_write` tool with an attached `knowledge` base. Do NOT use `write_file`; it is only for arbitrary host files and requires system authorization. The `kb_write` input must be a JSON object with `kb` and `content`. Prefer storing a structured artifact object such as {summary,tags,source,content}; pass it as `\"content\": {{ toJson .tagged_artifact }}` unquoted so markdown, quotes, and newlines stay valid JSON.\n")
	sb.WriteString("- For temporary state, queues, buffers, or handoffs between interactive workflow steps, use `queue_create`, `queue_put`, and `queue_take` instead of `write_file`. Queue tools are in-memory and safe for non-system agents; they are not durable across gateway restarts. Use an explicit queue name for multi-buffer workflows; otherwise the runtime uses the \"default\" queue.\n")
	if len(catalog.Providers) > 0 {
		sb.WriteString("Available providers: ")
		sb.WriteString(strings.Join(catalog.Providers, ", "))
		sb.WriteString("\n")
	}
	writeChannelGrounding(&sb, catalog)
	writeKBGrounding(&sb, catalog)
	writeCompositeBlockGrounding(&sb, intent)
	writePatternGrounding(&sb, intent, catalog)
	writePlanGrounding(&sb, intent, catalog)
	if len(answers) > 0 {
		sb.WriteString("\nThe user already answered these clarifying questions — honor them in the draft:\n")
		for _, k := range sortedKeys(answers) {
			v := strings.TrimSpace(answers[k])
			if v == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\n")
		}
	}

	// Final, explicit self-check — weaker models follow an end-of-prompt
	// checklist far better than rules buried in context. These are the failure
	// modes we keep seeing.
	sb.WriteString("\nFINAL CHECK before you output — fix any violation in your draft:\n")
	sb.WriteString("- Every VALUE handed from one tool/python step to a later tool/python step is a typed PORT WIRE (from_port/to_port on an edge with declared ports), NOT a {{ }} template. If you wrote {{ .something.id }} to feed a tool argument, convert it to a port wire.\n")
	sb.WriteString("- No node interpolates a WHOLE object: never {{ .x }} where a single value is needed — use the scalar field, e.g. {{ .x.id }} (or {{ toJson .x }} to send the whole object on purpose).\n")
	sb.WriteString("- No repeated/guessed nested path like {{ .x.x }} — reach the real field, e.g. {{ .x.id }}.\n")
	sb.WriteString("- No structured value is passed as \"key\": \"{{ .var }}\" (quoted) — that yields Go `map[...]` text, not JSON. Use \"key\": {{ toJson .var }} (unquoted) or leave a python node's input empty.\n")
	sb.WriteString("- Every {{ .var }} is produced by an EARLIER node's output; pass the RIGHT id (a status/poll step needs the resource id, not a sub-artifact id).\n")
	sb.WriteString("- Any poll/wait loop sets max_iterations (>1) on the back edge, or it only runs once.\n")
	sb.WriteString("- If the intent requests a finished artifact/report/storage/delivery, the graph contains the actual artifact/report/storage/delivery step(s), not just discovery/search.\n")
	sb.WriteString("- Exactly one entry; every path ends at \"end\"; a branch's last/fallback edge has an empty \"if\".\n")
	sb.WriteString("- Obey every AUTHORING RULE above. Re-read them and correct the draft before returning.\n")

	sb.WriteString("\nIntent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// sortedKeys returns the map keys in deterministic (sorted) order so the
// rendered prompt is stable across runs and unit-testable.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseDraft tolerantly extracts a Draft from raw model output: it strips
// ```/```json code fences and any leading/trailing prose around the first
// JSON object, then strictly unmarshals. A malformed payload yields a clear
// error.
func ParseDraft(raw string) (Draft, error) {
	s := stripFences(strings.TrimSpace(raw))
	// Narrow to the outermost JSON object if the model wrapped it in prose.
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return Draft{}, fmt.Errorf("studio: no JSON object found in model output")
	}
	s = s[start : end+1]

	// Tolerate the single most common model type-slip: a flow node's "input"
	// emitted as a JSON OBJECT/array instead of the schema's stringified JSON
	// (e.g. "input":{"query":"x"} rather than "input":"{\"query\":\"x\"}").
	// Without this, the whole otherwise-valid draft is thrown away with
	// `cannot unmarshal object into Go struct field FlowNode...input of type
	// string` — a frequent failure on weaker cloud models. Coerce, don't reject.
	s = coerceNodeInputs(s)

	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d Draft
	if err := dec.Decode(&d); err != nil {
		// Retry without strict field checking — be tolerant of extra keys
		// the model may add, but still fail loudly on structurally bad JSON.
		var d2 Draft
		if err2 := json.Unmarshal([]byte(s), &d2); err2 != nil {
			// Last tolerance: a raw newline inside a string literal. Node inputs
			// and system prompts are multi-line, and a model that writes them out
			// literally instead of escaping them produces JSON that is illegal by
			// one byte per line. Repair and retry rather than discard the graph.
			var d3 Draft
			if err3 := json.Unmarshal([]byte(escapeRawControlChars(s)), &d3); err3 == nil {
				return d3, nil
			}
			return Draft{}, fmt.Errorf("studio: parse draft: %w", err2)
		}
		return d2, nil
	}
	return d, nil
}

// coerceNodeInputs rewrites every flow node whose "input" the model emitted as a
// JSON object or array into the schema's stringified-JSON form, so a structurally
// reasonable draft that only mis-typed this one field still parses instead of
// being discarded. It is a pre-parse text transform: it round-trips the draft
// through a generic map, stringifies each offending node "input", and re-marshals.
// On any problem (not an object, unparseable, no flow/nodes) it returns the input
// unchanged so the normal decoder still surfaces the real error. Idempotent: a
// node "input" that is already a string is left alone.
func coerceNodeInputs(raw string) string {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw
	}
	flow, ok := root["flow"].(map[string]any)
	if !ok {
		return raw
	}
	nodes, ok := flow["nodes"].([]any)
	if !ok {
		return raw
	}
	changed := false
	for _, n := range nodes {
		nm, ok := n.(map[string]any)
		if !ok {
			continue
		}
		in, ok := nm["input"]
		if !ok {
			continue
		}
		switch in.(type) {
		case map[string]any, []any:
			if b, err := json.Marshal(in); err == nil {
				nm["input"] = string(b)
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	if b, err := json.Marshal(root); err == nil {
		return string(b)
	}
	return raw
}

// stripFences removes a single leading/trailing markdown code fence
// (```json … ``` or ``` … ```) if present.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (``` or ```json).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop the closing fence.
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// spec converts a Draft's flow into the sdk/reasoning FlowSpec for
// validation via reasoning.CompileFlow.
func (d Draft) spec() sdkr.FlowSpec {
	return sdkr.FlowSpec{
		Nodes: d.Flow.Nodes,
		Edges: d.Flow.Edges,
		Entry: d.Flow.Entry,
	}
}

// Compile runs the full pipeline: build a prompt, ask the model, parse the
// draft, validate the flow, and derive clarifying questions + notes. Hybrid:
// a structurally valid flow always yields a Result (draft + questions);
// only an unparseable response or a flow that fails CompileFlow is an error.
func Compile(ctx context.Context, llm LLM, intent string, catalog Catalog, answers map[string]string) (Result, error) {
	if strings.TrimSpace(intent) == "" {
		return Result{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return Result{}, fmt.Errorf("studio: no LLM configured")
	}

	prompt := BuildPrompt(intent, catalog, answers)
	// Schema-constrained generation when the client supports it: the builder
	// model is handed a JSON Schema that pins node.kind to the valid set at the
	// source (preventing the invent-a-"start"-node class), instead of relying on
	// the prompt alone and repairing after. Falls back to plain Complete.
	raw, err := completeDraft(ctx, llm, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("studio: llm complete: %w", err)
	}

	draft, err := ParseDraft(raw)
	if err != nil {
		if shouldRepairMalformedDraft(catalog.Generation) {
			if repairedRaw, repairErr := repairMalformedDraft(ctx, llm, prompt, raw, err); repairErr == nil {
				if repairedDraft, parseErr := ParseDraft(repairedRaw); parseErr == nil {
					draft = repairedDraft
					err = nil
				}
			}
		}
	}
	if err != nil {
		// Don't discard the evidence. Classify what the model ACTUALLY returned
		// (nothing / prose / truncated mid-object / malformed) so the operator
		// gets the real cause and the right fix instead of a raw JSON parser
		// error they can't act on.
		rd := DiagnoseRawOutput(raw)
		return Result{}, fmt.Errorf("studio: %s [%s, %d chars returned] model output: %s",
			rd.Reason, rd.Kind, rd.Chars, rd.Excerpt)
	}

	// Carry the user's original words onto the draft. Workflows do not ground
	// skills, so this is not about capability selection here — it is so the saved
	// agent records what was actually asked for (save.go persists it as
	// StudioRawIntent) and a later reload grounds against the request rather than
	// the refiner's expansion of it.
	if draft.RawIntent == "" {
		draft.RawIntent = strings.TrimSpace(catalog.RawIntent)
	}

	// Deterministic post-parse normalization: fill in an obvious trigger the
	// model left blank or mis-typed when the intent's phrasing clearly
	// implies one (S2.2). Modest and rule-based — it never overrides a
	// trigger the model already set correctly.
	normalizeTrigger(&draft, intent)

	// The operator's explicit channel choice wins over whatever the model named.
	ApplyChannelAnswers(&draft, answers)

	// Deterministic graph normalization (Story M3): make the node kinds the
	// model implied explicit, so the returned/persisted draft carries the same
	// tool|agent|branch kind the engine will infer. This never changes graph
	// SHAPE (nodes/edges/entry/ports stay as emitted) — it only fills a blank
	// Kind from the node's Tool/Agent fields, mirroring CompileFlow.
	normalizeFlow(&draft)

	// Reconcile any node whose DECLARED kind contradicts the fields it carries
	// (the classic weak-model slip: kind=python with neither inline code nor a
	// tool). CompileFlow is strict about this and would discard the whole
	// otherwise-valid graph over one mislabelled step; downgrade the step to a
	// kind it can actually satisfy instead. Whole-draft only — the per-node
	// compiler still rejects these, since there the user asked for that step.
	reconcileNodeKinds(&draft)

	// Auto-declare any edge-referenced port the model forgot to list on a node.
	// reasoning.CompileFlow is strict (a named from_port/to_port MUST appear in
	// the node's declared Outputs/Inputs), and models occasionally name a port
	// they didn't declare — which would otherwise throw away an entire
	// otherwise-valid draft over one cosmetic wiring slip.
	reconcilePorts(&draft)

	// Deterministic output-node guarantee: the reply the user reads must be a
	// content node, never a channel.send delivery receipt. Re-points a delivery
	// output to the content node feeding it. (Prompt steers the model here too;
	// this makes it certain regardless of model variance.)
	ensureContentOutput(&draft)

	// Deterministic data-flow repair (local-first pivot, Story #10): fill empty
	// required tool args from same-named upstream outputs AND reconcile dangling
	// {{ .var }} references to the right upstream output (e.g. a step referencing
	// {{ .date_str }} when its producer emits date_info) — repairing the most
	// common wiring failures without asking the model to regenerate.
	RepairWiring(&draft, catalog)

	// Classify each Custom Python node's required capabilities from its code so
	// the canvas can show them and save/consent can gate on them.
	classifyFlowNodes(&draft.Flow)

	// Parity (Phase E): seed each node's Intent from its description so a
	// generated node is re-editable as a prompt, exactly like a hand-built one.
	ensureNodeIntents(&draft.Flow)

	// Deterministic backstop for sub-agent quality: guarantee every agent node
	// that references a non-catalog helper agent has a full, reusable profile
	// (name + description + rich system prompt) in NewAgents — so a "Notifier"
	// or "Summarizer" the model invented (or forgot to define) is never saved
	// blank. Runs after normalizeFlow so node Kind/Agent are settled.
	ensureNewAgents(&draft, catalog)
	draft.SystemPrompt = agentprompt.EnsureShared(draft.SystemPrompt)

	// Focused-LLM repair (local-first pivot, Stories #16/#17): if deterministic
	// auto-wiring left a data-flow gap (a step references a var no earlier step
	// produces), ask the model to fix ONLY the broken node(s) — not regenerate
	// the whole agent — then re-run the deterministic passes over the result.
	// One pass, best-effort; the hard CompileFlow check below still guards.
	if focusedRepair(ctx, llm, &draft) {
		normalizeFlow(&draft)
		reconcilePorts(&draft)
		ensureContentOutput(&draft)
		AutoWire(&draft, catalog)
		classifyFlowNodes(&draft.Flow)
	}

	// The flow must compile — this is the hard contract. A model that emits a
	// structurally invalid multi-node/branch/port graph is rejected here and
	// NOT persisted; the caller sees a clear error.
	if !draft.IsAgent() {
		if _, err := reasoning.CompileFlow(draft.spec()); err != nil {
			return Result{}, fmt.Errorf("studio: compiled flow is invalid: %w", err)
		}
	}

	questions, notes := analyze(draft)
	if catalog.Generation != nil {
		gp := *catalog.Generation
		gp.PlanMatched = len(BuildPlan(intent, catalog)) > 0
		gp.PatternMatched = len(MatchPatterns(intent, catalog, 1)) > 0
		gp.LessonsApplied = len(catalog.Lessons)
		gp.Confidence, gp.NextAction = generationConfidence(gp)
		catalog.Generation = &gp
		if gp.Local && gp.Compact {
			notes = append(notes, "Local-model Studio mode: used deterministic planning, strict schema repair, and extra validation because the builder is a compact local model.")
		}
	}
	// Transparency (Stories #15/#10): tell the user which proven pattern(s)
	// shaped the design, so the applied step-ordering isn't a black box.
	if applied := MatchPatterns(intent, catalog, 2); len(applied) > 0 {
		names := make([]string, 0, len(applied))
		for _, p := range applied {
			names = append(names, p.Name)
		}
		notes = append(notes, "Applied proven pattern(s): "+strings.Join(names, "; ")+".")
	}
	// Ground read_skill nodes against the live index: fuzzy-correct a near-miss
	// skill name to the real installed one so the step resolves at run time
	// (symmetric with the agent path's skill grounding). Runs before usedSkills so
	// the corrected names flow into the transparency note + the saved Skills list.
	notes = append(notes, GroundFlowSkills(&draft, catalog)...)

	// Transparency (Stories #5/#6/#7): say which skills the agent will use and,
	// if a relevant KB exists that wasn't attached, recommend it.
	if sk := usedSkills(draft.Flow); len(sk) > 0 {
		notes = append(notes, "Uses installed skill(s): "+strings.Join(sk, ", ")+".")
	}
	if len(draft.Knowledge) > 0 {
		notes = append(notes, "Attached knowledge base(s): "+strings.Join(draft.Knowledge, ", ")+".")
	} else if rec := recommendKBs(intent, catalog); len(rec) > 0 {
		notes = append(notes, "Consider attaching knowledge base(s) for this task: "+strings.Join(rec, ", ")+".")
	}
	// Final gate: a draft that parsed but carries only empty placeholder steps
	// (or steps with nothing connecting them) is not a workflow — it's debris
	// from a builder model that couldn't hold the schema. Fail loudly here
	// instead of rendering a canvas of meaningless BRANCH nodes for the user to
	// debug.
	if reason := DegenerateReason(draft); reason != "" {
		return Result{}, fmt.Errorf("studio: the builder model did not produce a usable workflow: %s", reason)
	}

	explanation := ExplainDraft(draft)
	return Result{
		Workflow:    draft,
		Questions:   questions,
		Notes:       notes,
		Suggestions: suggestMissing(draft, catalog),
		Explanation: &explanation,
		Plan:        BuildPlan(intent, catalog),
		Generation:  catalog.Generation,
	}, nil
}

// suggestMissing inspects the (normalized) draft's flow and flags every tool
// and peer-agent it REFERENCES that is absent from the provided catalog, so
// the UI can offer to install/discover them (Stories S4.1/S4.3). It is a pure
// function — deterministic, side-effect-free, and unit-testable — so Compile
// can simply attach its output to the Result.
//
// Matching is tolerant: catalog entries and node references are compared after
// trimming surrounding whitespace and case-folding (a draft that says
// "HTTP_Fetch" matches a catalog "http_fetch"). The Catalog fields are plain
// string lists today; the comparison stays robust if they later carry
// decorated names.
//
// Design choice — actionable-only output: suggestMissing returns ONLY
// Installed:false entries (capabilities the draft needs but the catalog lacks).
// Present capabilities are intentionally omitted to keep the list a crisp
// to-do list for the UI rather than a full inventory. Callers that DO want the
// installed ones can use referencedCapabilities, which reports every
// referenced capability with its installed flag.
//
// Empty-catalog guard: if the caller supplied NO catalog context (no tools and
// no agents listed), suggestMissing returns nil. With nothing to compare
// against we cannot know what is or isn't installed, and emitting suggestions
// would be pure false positives. A caller wanting suggestions must pass the
// catalog of what they actually have.
func suggestMissing(draft Draft, cat Catalog) []Suggestion {
	all := referencedCapabilities(draft, cat)
	var out []Suggestion
	for _, s := range all {
		if !s.Installed {
			out = append(out, s)
		}
	}
	return out
}

// referencedCapabilities reports EVERY tool and peer-agent the draft's flow
// references, each tagged with whether it is present in the catalog
// (Installed). It is the shared core behind suggestMissing and is exported in
// spirit (lowercase, package-internal) for callers that want the full picture
// including already-installed capabilities. Like suggestMissing it honors the
// empty-catalog guard: with no catalog context it returns nil rather than
// guessing.
//
// References are de-duplicated and returned in deterministic order: all tools
// (in first-seen flow order) followed by all agents.
func referencedCapabilities(draft Draft, cat Catalog) []Suggestion {
	// Empty-catalog guard: no context => no claims about install state.
	if len(cat.Tools) == 0 && len(cat.Agents) == 0 && len(cat.MCP) == 0 {
		return nil
	}

	toolSet := newNameSet(cat.Tools)
	agentSet := newNameSet(cat.Agents)
	// MCP tools live in cat.MCP (full names like mcp__server__tool), NOT in
	// cat.Tools (which holds Go/Python builtins). Build a set of the connected
	// MCP tool names so an mcp__ tool node is recognised as installed when its
	// server is connected and exposes it. Without this, EVERY MCP tool node was
	// falsely flagged "not installed", even with the server connected.
	mcpSet := map[string]bool{}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if name := strings.TrimSpace(t.Name); name != "" {
				mcpSet[normalizeName(name)] = true
			}
		}
	}

	var out []Suggestion
	seenTool := map[string]bool{}
	seenAgent := map[string]bool{}

	// Tools first (first-seen flow order), then agents, for stable output.
	for _, n := range draft.Flow.Nodes {
		t := strings.TrimSpace(n.Tool)
		if t == "" || seenTool[normalizeName(t)] {
			continue
		}
		key := normalizeName(t)
		seenTool[key] = true
		installed := toolSet[key]
		// An mcp__server__tool reference is installed when a connected MCP server
		// exposes it (checked against cat.MCP, not cat.Tools).
		if !installed && strings.HasPrefix(strings.ToLower(t), "mcp__") {
			installed = mcpSet[key]
		}
		out = append(out, Suggestion{
			Kind:      "tool",
			Name:      t,
			Installed: installed,
			Reason:    capabilityReason("tool", t, installed),
		})
	}
	for _, n := range draft.Flow.Nodes {
		a := strings.TrimSpace(n.Agent)
		if a == "" || seenAgent[normalizeName(a)] {
			continue
		}
		seenAgent[normalizeName(a)] = true
		installed := agentSet[normalizeName(a)]
		out = append(out, Suggestion{
			Kind:      "agent",
			Name:      a,
			Installed: installed,
			Reason:    capabilityReason("agent", a, installed),
		})
	}
	return out
}

// capabilityReason renders a helpful, human-readable reason for a suggestion.
func capabilityReason(kind, name string, installed bool) string {
	if installed {
		return fmt.Sprintf("workflow references %s %q which is already installed", kind, name)
	}
	return fmt.Sprintf("workflow references %s %q which isn't installed", kind, name)
}

// newNameSet builds a lookup set of normalized capability names from a catalog
// list. Entries are case-folded and trimmed so matching is tolerant.
func newNameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		set[normalizeName(n)] = true
	}
	return set
}

// normalizeName canonicalizes a capability name for tolerant matching.
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// normalizeTrigger applies modest, DETERMINISTIC trigger inference (Story
// S2.2): it inspects the plain-language intent and fills or corrects the
// draft's Trigger when the phrasing clearly implies a kind the model left
// blank, mis-typed, or under-specified. It is intentionally conservative:
//
//   - It never changes a trigger the model already set to a recognized type
//     UNLESS that trigger is a schedule missing its cron and the intent
//     supplies an unambiguous cadence (then it fills trigger.config.cron).
//   - Schedule phrasings ("every morning", "every weekday", "daily",
//     "at 8am", "hourly", …) map to type=schedule with a sane cron.
//   - Channel phrasings ("when someone messages", "on telegram", "reply
//     to messages") map to type=channel.
//   - Webhook phrasings ("webhook", "when X posts to", "on POST") map to
//     type=webhook.
//
// Anything ambiguous is left untouched so analyze() can raise a clarifying
// question. This is not NL parsing — it is a small keyword table.
func normalizeTrigger(d *Draft, intent string) {
	if d == nil {
		return
	}
	lc := strings.ToLower(intent)
	typ := strings.ToLower(strings.TrimSpace(d.Trigger.Type))
	known := typ == "schedule" || typ == "channel" || typ == "webhook" || typ == "manual"

	inferred := inferTriggerType(lc)

	// Fill a missing/unrecognized type from the intent when we can.
	if !known && inferred != "" {
		d.Trigger.Type = inferred
		typ = inferred
		known = true
	}

	// For schedule triggers (whether the model set them or we just did),
	// ensure a cron is present: derive one from the intent if the model
	// left config.cron blank.
	if typ == "schedule" {
		cron, _ := d.Trigger.Config["cron"].(string)
		if strings.TrimSpace(cron) == "" {
			if c := inferCron(lc); c != "" {
				if d.Trigger.Config == nil {
					d.Trigger.Config = map[string]any{}
				}
				d.Trigger.Config["cron"] = c
			}
		}
	}

	// A still-empty/unknown type with no schedule/channel/webhook signal is
	// left alone — analyze() asks the user. Avoid guessing "manual" here.
	_ = known
}

// inferTriggerType returns the trigger type implied by the intent, or "" when
// no clear signal is present. Schedule wins over channel/webhook when both
// appear, because a cadence phrase ("every morning … on telegram") describes
// WHEN it runs while the channel is the OUTPUT.
func inferTriggerType(lc string) string {
	scheduleCues := []string{
		"every morning", "every weekday", "every day", "everyday",
		"each morning", "each day", "daily", "weekly", "hourly",
		"every hour", "every monday", "every week", "at 8am", "at 8 am",
		"at noon", "at midnight", "schedule", "cron", "o'clock",
	}
	for _, cue := range scheduleCues {
		if strings.Contains(lc, cue) {
			return "schedule"
		}
	}
	// Generic "every <time>" / "at <n>(am|pm)" cadence.
	if hasAtClock(lc) {
		return "schedule"
	}

	webhookCues := []string{"webhook", "on post", "posts to", "http callback", "incoming request"}
	for _, cue := range webhookCues {
		if strings.Contains(lc, cue) {
			return "webhook"
		}
	}

	channelCues := []string{
		"when someone messages", "when someone sends", "on telegram",
		"on slack", "on discord", "on whatsapp", "reply to messages",
		"when a message", "when i message", "respond to messages",
		"someone dms", "incoming message", "receives a user message",
		"receive a user message", "receives user messages", "user queries",
		"query arrived", "same channel", "channel the query arrived",
	}
	for _, cue := range channelCues {
		if strings.Contains(lc, cue) {
			return "channel"
		}
	}
	return ""
}

// inferCron maps common cadence phrasings onto a cron expression. Returns ""
// when the cadence is present-but-unspecified (e.g. bare "every day" with no
// time) so the caller leaves cron blank and analyze() asks for the time.
func inferCron(lc string) string {
	hour := inferHour(lc)

	switch {
	case strings.Contains(lc, "every weekday") || strings.Contains(lc, "weekdays"):
		if hour < 0 {
			hour = 8
		}
		return cronExpr(hour, "1-5")
	case strings.Contains(lc, "every morning") || strings.Contains(lc, "each morning"):
		if hour < 0 {
			hour = 8
		}
		return cronExpr(hour, "*")
	case strings.Contains(lc, "hourly") || strings.Contains(lc, "every hour"):
		return "0 * * * *"
	case strings.Contains(lc, "daily") || strings.Contains(lc, "every day") ||
		strings.Contains(lc, "everyday") || strings.Contains(lc, "each day"):
		if hour < 0 {
			return "" // cadence is clear, time is not — let analyze() ask.
		}
		return cronExpr(hour, "*")
	case strings.Contains(lc, "at midnight"):
		return cronExpr(0, "*")
	case strings.Contains(lc, "at noon"):
		return cronExpr(12, "*")
	}

	// Bare "at 8am" with no cadence word implies a daily schedule.
	if hour >= 0 {
		return cronExpr(hour, "*")
	}
	return ""
}

// cronExpr builds a "minute hour * * dow" cron at minute 0.
func cronExpr(hour int, dow string) string {
	return fmt.Sprintf("0 %d * * %s", hour, dow)
}

// hasAtClock reports whether the intent contains an "at <n>(am|pm)" or
// "<n> o'clock" time signal.
func hasAtClock(lc string) bool {
	return inferHour(lc) >= 0
}

// inferHour extracts a 0-23 hour from phrasings like "at 8am", "at 8 am",
// "8pm", "at noon", "at midnight". Returns -1 when no hour is found.
func inferHour(lc string) int {
	if strings.Contains(lc, "midnight") {
		return 0
	}
	if strings.Contains(lc, "noon") {
		return 12
	}
	// Scan for "<digits> am/pm" or "at <digits>".
	for i := 0; i < len(lc); i++ {
		if lc[i] < '0' || lc[i] > '9' {
			continue
		}
		j := i
		for j < len(lc) && lc[j] >= '0' && lc[j] <= '9' {
			j++
		}
		n := 0
		for k := i; k < j; k++ {
			n = n*10 + int(lc[k]-'0')
		}
		rest := strings.TrimLeft(lc[j:], " ")
		switch {
		case strings.HasPrefix(rest, "am"):
			if n >= 1 && n <= 12 {
				if n == 12 {
					n = 0
				}
				return n
			}
		case strings.HasPrefix(rest, "pm"):
			if n >= 1 && n <= 12 {
				if n != 12 {
					n += 12
				}
				return n
			}
		case strings.HasPrefix(rest, "o'clock") || strings.HasPrefix(rest, "oclock"):
			if n >= 0 && n <= 23 {
				return n
			}
		}
		// Plain 24h hour right after "at " (e.g. "at 14:00").
		if i >= 3 && lc[i-3:i] == "at " && n >= 0 && n <= 23 {
			// Only treat as hour if followed by ":" or end/space, to avoid
			// matching quantities like "at 5 stories".
			if j >= len(lc) || lc[j] == ':' {
				return n
			}
		}
		i = j
	}
	return -1
}

// normalizeFlow makes the implied node kinds explicit (Story M3). For each
// node whose Kind is blank it derives tool|agent|branch from the node's
// Tool/Agent fields — the exact same inference reasoning.CompileFlow performs
// — so the draft the compiler returns (and the one Studio later persists)
// carries the kind the engine will execute. It is purely additive: it never
// rewrites a Kind the model already set, never touches edges/ports/entry, and
// never invents nodes. Branch and multi-agent graphs round-trip unchanged.
func normalizeFlow(d *Draft) {
	if d == nil {
		return
	}
	for i := range d.Flow.Nodes {
		n := &d.Flow.Nodes[i]
		// Unwrap a double-wrapped python code field: some models emit the node's
		// `code` as a JSON envelope {"code":"import …"} instead of raw Python, so
		// the stored Code is `{"code":"..."}` — which fails at run time with
		// "name 'run' is not defined". Replace it with the inner source.
		unwrapNodeCode(n)

		// Canonicalize invented kind synonyms to the valid structural kinds so a
		// generated "start"/"end" node never fails the strict compile. This is the
		// single deterministic place LLM kind-variance is absorbed.
		if k := canonicalNodeKind(n.Kind); k != "" {
			n.Kind = k
		}

		if strings.TrimSpace(n.Kind) != "" {
			continue
		}
		switch {
		case strings.TrimSpace(n.Tool) != "":
			n.Kind = sdkr.FlowNodeTool
		case strings.TrimSpace(n.Agent) != "":
			n.Kind = sdkr.FlowNodeAgent
		case strings.TrimSpace(n.Code) != "":
			n.Kind = sdkr.FlowNodePython
		default:
			n.Kind = sdkr.FlowNodeBranch
		}
	}
}

// reconcileNodeKinds repairs every node in a WHOLE-DRAFT whose declared Kind
// can't be satisfied by the fields it actually carries.
//
// This is deliberately NOT part of normalizeFlow: the per-node compiler
// (CompileNode, "describe this one step") must still REJECT a node that names
// no tool, because there the user asked for one specific step and silently
// downgrading it would be wrong. But when generating a whole workflow, one
// mislabelled step must not discard the entire otherwise-valid draft — the
// strict compiler would throw away a good graph over a single slip.
func reconcileNodeKinds(d *Draft) {
	if d == nil {
		return
	}
	for i := range d.Flow.Nodes {
		reconcileNodeKind(&d.Flow.Nodes[i])
	}
}

// reconcileNodeKind repairs a node whose declared Kind can't be satisfied by the
// fields it actually carries. CompileFlow is strict — kind=tool MUST name a
// tool, kind=agent MUST name an agent, kind=python MUST have inline code or a
// python tool.
//
// The repair is deterministic and always DOWNGRADES to a kind the node can
// actually satisfy. A step that names no tool, agent, or code but still has an
// intent (e.g. "format_response") is an LLM transform: kind=llm needs no extra
// field and does exactly that work, so the flow stays valid and the step keeps
// doing what the model meant.
func reconcileNodeKind(n *sdkr.FlowNode) {
	hasTool := strings.TrimSpace(n.Tool) != ""
	hasAgent := strings.TrimSpace(n.Agent) != ""
	hasCode := strings.TrimSpace(n.Code) != ""

	switch strings.ToLower(strings.TrimSpace(n.Kind)) {
	case sdkr.FlowNodePython:
		// Legal as python only with inline code OR a deployed python tool.
		if hasCode || hasTool {
			return
		}
		if hasAgent {
			n.Kind = sdkr.FlowNodeAgent
			return
		}
		n.Kind = sdkr.FlowNodeLLM

	case sdkr.FlowNodeTool:
		if hasTool {
			return
		}
		switch {
		case hasCode:
			n.Kind = sdkr.FlowNodePython
		case hasAgent:
			n.Kind = sdkr.FlowNodeAgent
		default:
			n.Kind = sdkr.FlowNodeLLM
		}

	case sdkr.FlowNodeAgent:
		if hasAgent {
			return
		}
		switch {
		case hasTool:
			n.Kind = sdkr.FlowNodeTool
		case hasCode:
			n.Kind = sdkr.FlowNodePython
		default:
			n.Kind = sdkr.FlowNodeLLM
		}
	}
}

// deliveryTools are pure out-of-band delivery tools whose result is a receipt,
// not content — they must never be a flow's output node.
var deliveryTools = map[string]bool{"channel.send": true}

func isDeliveryNode(n sdkr.FlowNode) bool {
	return strings.EqualFold(n.Kind, sdkr.FlowNodeTool) && deliveryTools[strings.TrimSpace(n.Tool)]
}

func isContentNode(n sdkr.FlowNode) bool {
	switch strings.ToLower(strings.TrimSpace(n.Kind)) {
	case sdkr.FlowNodeAgent, sdkr.FlowNodeLLM, sdkr.FlowNodePython:
		return true
	case sdkr.FlowNodeTool:
		return !deliveryTools[strings.TrimSpace(n.Tool)]
	default:
		return false // branch/trigger/exit produce no content
	}
}

// ensureContentOutput enforces the rule that a flow's OUTPUT (the reply the user
// reads) is a CONTENT node, never a delivery node like channel.send whose result
// is a {"ok":true,...} receipt. Deterministic: if the designated/terminal output
// is a delivery node, it re-points Output to the content node feeding it (or the
// last content node). This is the authoritative guarantee — the runtime
// receipt→content fallback is only defense-in-depth for older saved flows.
func ensureContentOutput(d *Draft) {
	if d == nil || len(d.Flow.Nodes) == 0 {
		return
	}
	idx := make(map[string]*sdkr.FlowNode, len(d.Flow.Nodes))
	for i := range d.Flow.Nodes {
		idx[d.Flow.Nodes[i].ID] = &d.Flow.Nodes[i]
	}
	outID := strings.TrimSpace(d.Flow.Output)
	if outID == "" {
		outID = d.Flow.Nodes[len(d.Flow.Nodes)-1].ID
	}
	n := idx[outID]
	if n == nil || !isDeliveryNode(*n) {
		return // output is already content (or unknown) — leave it
	}
	// Prefer the content node that feeds the delivery node.
	for _, e := range d.Flow.Edges {
		if e.To == outID {
			if pred, ok := idx[e.From]; ok && isContentNode(*pred) {
				d.Flow.Output = pred.ID
				return
			}
		}
	}
	// Fall back to the last content node in the graph.
	for i := len(d.Flow.Nodes) - 1; i >= 0; i-- {
		if isContentNode(d.Flow.Nodes[i]) {
			d.Flow.Output = d.Flow.Nodes[i].ID
			return
		}
	}
}

// canonicalNodeKind maps kind synonyms a builder model sometimes emits to the
// valid flow kinds. Returns "" when the kind is already valid/blank (leave it).
// This is the authoritative, deterministic normalization — CompileFlow's own
// tolerance is only a defense-in-depth net for older saved flows.
func canonicalNodeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "start", "entry", "begin", "receive", "input_node", "trigger_node":
		return sdkr.FlowNodeTrigger
	case "end", "finish", "done", "output_node", "exit_node":
		return sdkr.FlowNodeExit
	case "llm_extract", "extract", "extractor":
		return sdkr.FlowNodeLLM
	default:
		return ""
	}
}

// reconcilePorts makes every edge's port references self-consistent so the
// strict reasoning.CompileFlow port check passes. For each edge with a named
// from_port/to_port that the referenced node did not declare, we ADD that port
// to the node's Outputs/Inputs (preserving the model's intended named handle)
// rather than failing the compile. Edges to the terminal ("end"/"") have no
// target node and are skipped naturally by the id lookup. Idempotent.
func reconcilePorts(d *Draft) {
	if d == nil {
		return
	}
	idx := make(map[string]*sdkr.FlowNode, len(d.Flow.Nodes))
	for i := range d.Flow.Nodes {
		idx[d.Flow.Nodes[i].ID] = &d.Flow.Nodes[i]
	}
	ensure := func(ports *[]sdkr.FlowPort, name string) {
		if name == "" {
			return
		}
		for _, port := range *ports {
			if port.Name == name {
				return
			}
		}
		*ports = append(*ports, sdkr.FlowPort{Name: name})
	}
	for _, e := range d.Flow.Edges {
		if n, ok := idx[e.From]; ok {
			ensure(&n.Outputs, e.FromPort)
		}
		if n, ok := idx[e.To]; ok {
			ensure(&n.Inputs, e.ToPort)
		}
	}
}

// ensureNodeIntents gives every non-structural node a plain-language Intent
// (Phase E parity): a generated node seeds its Intent from its Description so it
// is re-editable as a prompt and round-trips identically to a node the user
// authored by "describe this step." Never overwrites an Intent the node already
// carries; skips trigger/exit/branch (structural) nodes.
func ensureNodeIntents(f *Flow) {
	if f == nil {
		return
	}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		if sdkr.IsStructuralKind(n.Kind) {
			continue
		}
		if strings.TrimSpace(n.Intent) == "" {
			n.Intent = strings.TrimSpace(n.Description)
		}
	}
}

// classifyFlowNodes sets Requires on every Custom Python node from its inline
// Code (internal/studio/codeclass). Deterministic and idempotent; a node with
// no code, or pure-data code, ends up with no Requires (ReadOnly). Nodes that
// reference a deployed tool (no inline Code) are left untouched.
func classifyFlowNodes(f *Flow) {
	if f == nil {
		return
	}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		if n.Kind != sdkr.FlowNodePython || strings.TrimSpace(n.Code) == "" {
			continue
		}
		n.Requires = codeclass.Classify(n.Code).Requires
	}
}

// analyze derives clarifying questions for missing essentials and notes
// explaining what the compiler inferred. It never blocks: a draft with gaps
// still produces a Result, with questions describing the gaps.
func analyze(d Draft) ([]Question, []string) {
	var questions []Question
	var notes []string

	notes = append(notes, fmt.Sprintf("Inferred trigger type %q from the intent.", d.Trigger.Type))

	switch d.Trigger.Type {
	case "schedule":
		cron, _ := d.Trigger.Config["cron"].(string)
		if strings.TrimSpace(cron) == "" {
			questions = append(questions, Question{
				ID:   "schedule_time",
				Text: "When should this run? Provide a time or cron schedule.",
			})
			notes = append(notes, "No schedule time was specified; asking for one.")
		} else {
			notes = append(notes, fmt.Sprintf("Scheduled with cron %q.", cron))
		}
	case "channel", "webhook", "manual":
		notes = append(notes, fmt.Sprintf("Trigger %q needs no schedule.", d.Trigger.Type))
	default:
		questions = append(questions, Question{
			ID:      "trigger_type",
			Text:    "How should this workflow be triggered?",
			Options: []string{"schedule", "channel", "webhook", "manual"},
		})
		notes = append(notes, "Trigger type was missing or unrecognized; asking how to start the workflow.")
	}

	if len(d.Channels) == 0 {
		questions = append(questions, Question{
			ID:      "output_channel",
			Text:    "Where should the results be delivered?",
			Options: []string{"telegram", "slack", "discord", "email"},
		})
		notes = append(notes, "No output channel was specified; asking where results go.")
	} else {
		notes = append(notes, fmt.Sprintf("Delivering to channels: %s.", strings.Join(d.Channels, ", ")))
	}

	notes = append(notes, fmt.Sprintf("Flow has %d node(s) entering at %q.", len(d.Flow.Nodes), d.Flow.Entry))

	return questions, notes
}

// escapeRawControlChars escapes literal control characters that appear INSIDE a
// JSON string literal, so a payload that is illegal by one byte per line parses
// instead of being thrown away.
//
// JSON forbids a raw newline between quotes; it must be written \n. Models
// routinely emit the real byte, because the values here are exactly the
// multi-line ones — a system prompt, a node input carrying an embedded
// document, a description written as a list. One un-escaped newline invalidates
// the entire object.
//
// Observed live building a multi-agent workflow: the builder returned
// "parse agent spec: invalid character '\n' in string literal", the whole graph
// was discarded, and the run fell back to a deterministic two-node skeleton with
// no agents in it. The user's structure was lost to a quoting slip.
//
// Only bytes inside a string literal are touched, and only ones that are
// ILLEGAL there. Valid JSON contains no such bytes, so this is a no-op on it —
// which is why callers can safely apply it as a retry after a parse failure
// without risking a different interpretation of a document that already parsed.
//
// Escapes are tracked so a backslash-escaped quote does not look like the end
// of the string; getting that wrong would corrupt every value after the first
// escaped quote.
func escapeRawControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		switch {
		case c == '\\' && inString:
			// A backslash that does not begin a legal escape. JSON allows only
			// \" \\ \/ \b \f \n \r \t and \uXXXX; anything else is a parse
			// error. Models produce these constantly — "\-" from markdown
			// escaping, "\d" from a regex, a Windows path — and each one costs
			// the whole document. Observed live: "parse agent spec: invalid
			// character '-' in string escape code" discarded a generated graph.
			//
			// A backslash the model did not mean as an escape is a literal
			// backslash, so write it as one.
			if validJSONEscape(s, i+1) {
				b.WriteByte(c)
				escaped = true
			} else {
				b.WriteString(`\\`)
			}
		case c == '"':
			inString = !inString
			b.WriteByte(c)
		case inString && c < 0x20:
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// validJSONEscape reports whether the byte at i begins a legal JSON escape.
//
// \u is only legal with four hex digits behind it, so a truncated "\u12" is
// treated as a literal backslash rather than passed through to fail the parse.
func validJSONEscape(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	switch s[i] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if i+4 >= len(s) {
			return false
		}
		for j := i + 1; j <= i+4; j++ {
			c := s[j]
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
		return true
	}
	return false
}
