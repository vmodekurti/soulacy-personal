package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// PromptRefinement is the result of the pre-generation refine pass. Before the
// compiler turns an intent into a workflow, RefinePrompt asks the framework LLM
// to act as a requirements analyst: it rewrites the user's plain-language intent
// into a clear, complete, unambiguous specification, states the assumptions it
// had to make, and surfaces clarifying questions for the genuinely ambiguous,
// decision-changing gaps. The UI shows all of this and lets the user confirm or
// edit BEFORE a workflow is generated — so a vague prompt no longer silently
// produces a broken workflow.
type PromptRefinement struct {
	// Original is the raw intent the user typed (echoed back for the UI diff).
	Original string `json:"original"`
	// RefinedIntent is the rewritten, self-contained specification. It is what
	// the compiler should be fed once the user confirms — every piece spelled
	// out: trigger/schedule, data sources, processing steps, output channels,
	// and edge-case handling.
	RefinedIntent string `json:"refined_intent"`
	// Summary is a one- or two-sentence plain-language description of what the
	// resulting automation will do, so the user understands what they are
	// signing up for at a glance.
	Summary string `json:"summary"`
	// Assumptions lists the decisions the analyst made to fill gaps in the
	// original intent (e.g. "Assumed a daily 8am schedule", "Assumed output to
	// Telegram"). The user can correct any of these by editing the refined
	// intent before generating.
	Assumptions []string `json:"assumptions"`
	// Questions are clarifying questions for the genuinely ambiguous gaps that
	// would change the workflow. The UI renders them; answers are woven into the
	// compile that follows. Empty when the intent is already clear enough.
	Questions []Question `json:"questions"`
	// RecommendedMode is the architecture the analyst judges best: "workflow"
	// (fixed pipeline), "auto" (normal tool-calling agent), "react" (explicit
	// reasoning loop, advanced/manual only), or "plan_execute". The wizard uses
	// it to decide whether Generate produces a flow or an agent. ModeReason is a
	// one-line justification.
	RecommendedMode string `json:"recommended_mode"`
	ModeReason      string `json:"mode_reason"`
}

// refinePromptPayload is the exact JSON shape the model is told to return. It is
// kept separate from PromptRefinement so the wire contract is explicit and the
// model never has to know about the server-filled Original field.
type refinePromptPayload struct {
	RefinedIntent   string     `json:"refined_intent"`
	Summary         string     `json:"summary"`
	Assumptions     []string   `json:"assumptions"`
	Questions       []Question `json:"questions"`
	RecommendedMode string     `json:"recommended_mode"`
	ModeReason      string     `json:"mode_reason"`
}

// BuildRefinePromptInstruction builds the instruction for the refine pass. It is
// pure (no I/O) and deterministic so it is unit-testable, and it grounds the
// analyst in the SAME live catalog the compiler will use — so the refined
// intent only references capabilities that actually exist.
func BuildRefinePromptInstruction(intent string, catalog Catalog) string {
	// Trim large grounding lists to the intent-relevant subset (no-op when small).
	catalog = FilterCatalogForIntent(intent, catalog)
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio requirements analyst. ")
	sb.WriteString("A user has described an automation they want built. Your job is NOT to build it yet — ")
	sb.WriteString("it is to turn their rough, often vague description into a clear, complete, unambiguous specification, ")
	sb.WriteString("and to flag anything still genuinely unclear, BEFORE a workflow is generated.\n\n")

	sb.WriteString("Why this matters: a vague prompt produces a broken or wrong workflow. Every piece of the spec must be explicit.\n\n")

	sb.WriteString("Produce a refined specification that pins down ALL of:\n")
	sb.WriteString("1. TRIGGER — when/how it runs: a schedule (give a concrete cadence, e.g. \"every weekday at 8am\"), an incoming message/channel, a webhook, or manual.\n")
	sb.WriteString("2. INPUTS / DATA SOURCES — exactly what data it works on and where that comes from (a search query, an API, an uploaded file, an MCP server).\n")
	sb.WriteString("3. PROCESSING STEPS — the concrete sequence of work, in order, in plain language.\n")
	sb.WriteString("4. OUTPUT — what is produced and where it goes (which channel: telegram/slack/email, a file, etc.).\n")
	sb.WriteString("5. EDGE CASES — what to do on empty results, errors, or nothing-to-report.\n\n")

	sb.WriteString("Rules:\n")
	sb.WriteString("- Stay faithful to the user's intent. Do NOT invent scope they did not ask for; fill only the gaps needed to make it buildable.\n")
	sb.WriteString("- Where you must make a choice to fill a gap, pick a sensible default AND record it in \"assumptions\" so the user can correct it.\n")
	sb.WriteString("- Only reference capabilities that exist in the catalog below. If the user names something not available, note it as an assumption or a question rather than inventing it.\n")
	sb.WriteString("- Ask a clarifying question ONLY when the answer would genuinely change the workflow (a real fork). Do not ask about things you can reasonably default. Prefer 0–3 high-value questions; an already-clear intent needs none.\n")
	sb.WriteString("- The \"refined_intent\" must be self-contained: a person reading ONLY it should understand the whole automation. Write it as clear prose or a short ordered list, not JSON.\n")
	sb.WriteString("- \"summary\" is one or two plain sentences describing what the automation will do.\n\n")

	writeUnifiedArchitectureGuidance(&sb)

	sb.WriteString("Respond with ONLY a single JSON object, no prose, no markdown, no code fences, matching exactly:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"refined_intent\": \"<the complete, unambiguous specification>\",\n")
	sb.WriteString("  \"summary\": \"<one or two sentences: what this automation does>\",\n")
	sb.WriteString("  \"assumptions\": [\"<each gap you filled and the default you chose>\"],\n")
	sb.WriteString("  \"questions\": [ { \"id\": \"<short_id>\", \"text\": \"<question>\", \"options\": [\"<opt>\", \"...\"] } ],\n")
	sb.WriteString("  \"recommended_mode\": \"workflow|auto|react|plan_execute\",\n")
	sb.WriteString("  \"mode_reason\": \"<1 sentence on why this architecture fits>\"\n")
	sb.WriteString("}\n")
	sb.WriteString("(\"options\" is optional — include it only when the answer is a closed choice. \"assumptions\" and \"questions\" may be empty arrays.)\n\n")

	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)
	sb.WriteString(WorkflowPatternsPromptBlock(catalog.WorkflowPatterns))
	sb.WriteString(UnreliableStrategiesPromptBlock(catalog.ActiveModel, catalog.UnreliableStrategies))
	sb.WriteString(GlobalPreferencesPromptBlock(catalog.GlobalPreferences))

	sb.WriteString("\nUser's original intent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

func writeUnifiedArchitectureGuidance(sb *strings.Builder) {
	sb.WriteString("Also decide the best ARCHITECTURE using the same rule Studio uses for generation, and return it:\n")
	sb.WriteString("- \"workflow\": a fixed, deterministic pipeline: the same steps in the same order every run, knowable up front (e.g. each morning search X, summarize, post to Telegram).\n")
	sb.WriteString("- \"auto\": the recommended default for a conversational or tool-using agent that decides which available tool to call at run time (e.g. weather assistant, flight finder, research assistant, deal finder). The engine runs it as a native tool-calling loop with no fixed graph.\n")
	sb.WriteString("- \"react\": an advanced/manual escape hatch ONLY when the user explicitly asks for ReAct, a think-act-observe loop, or a classic reasoning-loop experiment. Do not choose it automatically.\n")
	sb.WriteString("- \"plan_execute\": a long, multi-phase job where the agent should make a plan first and then execute the plan.\n")
	sb.WriteString("Do NOT choose \"react\" merely because the agent uses tools, loops over items, polls jobs, or does research. Ordinary tool use should be \"auto\"; long adaptive work should be \"plan_execute\"; fixed scheduled pipelines should be \"workflow\".\n\n")
}

// writeCatalogGrounding appends the available-capabilities context (skills, MCP
// servers/tools, agents, channels) to sb. It is shared by the refine pass and
// could be reused by other prompt builders; it mirrors the grounding format the
// compiler uses so the analyst and the compiler see the same world.
func writeCatalogGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.Skills) > 0 {
		sb.WriteString("Available skills (data sources / capabilities you may reference):\n")
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
	if len(catalog.MCP) > 0 {
		sb.WriteString("Available MCP servers and their tools:\n")
		for _, srv := range catalog.MCP {
			name := strings.TrimSpace(srv.Server)
			if name == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			sb.WriteString("\n")
			for _, t := range srv.Tools {
				tn := strings.TrimSpace(t.Name)
				if tn == "" {
					continue
				}
				desc := strings.TrimSpace(t.Description)
				if len(desc) > 200 {
					desc = desc[:200] + "…"
				}
				sb.WriteString("    • ")
				sb.WriteString(tn)
				if desc != "" {
					sb.WriteString(" — ")
					sb.WriteString(desc)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	if len(catalog.Agents) > 0 {
		sb.WriteString("Available agents: ")
		sb.WriteString(strings.Join(catalog.Agents, ", "))
		sb.WriteString("\n")
	}
	if len(catalog.Tools) > 0 {
		sb.WriteString("Available tools: ")
		sb.WriteString(strings.Join(catalog.Tools, ", "))
		sb.WriteString("\n")
	}
	writeChannelGrounding(sb, catalog)
	writeKBGrounding(sb, catalog)
}

// writeChannelGrounding appends the configured output channels so prompts wire
// delivery to a real channel instead of inventing one. Shared by compile +
// refine.
func writeChannelGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.Channels) == 0 {
		return
	}
	sb.WriteString("Configured output channels (deliver results to one of these EXACT names): ")
	sb.WriteString(strings.Join(catalog.Channels, ", "))
	sb.WriteString("\n")
}

// writeKBGrounding appends the available knowledge bases with their
// descriptions so the compiler can attach a relevant KB to the agent (Story
// #7). Shared by compile + refine.
func writeKBGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.KnowledgeBases) == 0 {
		return
	}
	sb.WriteString("Available knowledge bases — to give the agent access to one, add its EXACT name to a top-level \"knowledge\" array in your JSON. Attach a KB ONLY when its subject clearly matches the intent; never attach unrelated KBs:\n")
	for _, kb := range catalog.KnowledgeBases {
		name := strings.TrimSpace(kb.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(kb.Description)
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

// BuildLightRefineInstruction builds the instruction for a LIGHT (touch-up)
// refine pass. It is used when the intent has ALREADY been through a full
// refine and the user has hand-edited the resulting specification: re-running
// the full analyst rewrite would be slow and would fight the user's edits. The
// light pass instead treats the input as near-final — it only cleans up grammar
// and obvious gaps, preserves the user's wording and structure, and returns the
// same JSON shape so the UI/compiler path is unchanged. Like the full builder it
// is pure and grounds the model in the same catalog.
func BuildLightRefineInstruction(intent string, catalog Catalog) string {
	catalog = FilterCatalogForIntent(intent, catalog)
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio requirements analyst doing a LIGHT touch-up. ")
	sb.WriteString("The text below is ALREADY a refined specification that the user has reviewed and hand-edited. ")
	sb.WriteString("It is essentially final. Do NOT rewrite it, restructure it, or expand its scope.\n\n")

	sb.WriteString("Your ONLY job is a light cleanup that respects the user's edits:\n")
	sb.WriteString("- Fix grammar, spelling, and obvious clarity issues.\n")
	sb.WriteString("- Preserve the user's wording, ordering, and intent as closely as possible — change as little as you can.\n")
	sb.WriteString("- If the user left an obvious gap in the standard spec (trigger, inputs, processing, output, edge cases), fill ONLY that gap with a sensible default and record it in \"assumptions\".\n")
	sb.WriteString("- Do NOT introduce new features, steps, or scope the user did not write.\n")
	sb.WriteString("- Only reference capabilities that exist in the catalog below.\n")
	sb.WriteString("- Ask a clarifying question ONLY if the user's edit introduced a genuine, workflow-changing contradiction. Prefer 0 questions.\n\n")

	writeUnifiedArchitectureGuidance(&sb)

	sb.WriteString("Respond with ONLY a single JSON object, no prose, no markdown, no code fences, matching exactly:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"refined_intent\": \"<the lightly cleaned-up specification — keep the user's text>\",\n")
	sb.WriteString("  \"summary\": \"<one or two sentences: what this automation does>\",\n")
	sb.WriteString("  \"assumptions\": [\"<only gaps you had to fill>\"],\n")
	sb.WriteString("  \"questions\": [ { \"id\": \"<short_id>\", \"text\": \"<question>\", \"options\": [\"<opt>\", \"...\"] } ],\n")
	sb.WriteString("  \"recommended_mode\": \"workflow|auto|react|plan_execute\",\n")
	sb.WriteString("  \"mode_reason\": \"<1 sentence on why this architecture fits>\"\n")
	sb.WriteString("}\n")
	sb.WriteString("(\"assumptions\" and \"questions\" may be empty arrays.)\n\n")

	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)

	sb.WriteString("\nAlready-refined specification (the user's edited text):\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// RefinePrompt runs the pre-generation refine pass: it asks the LLM to rewrite
// the intent into a clear specification plus assumptions and clarifying
// questions. Like Compile it is tolerant of model output wrapped in fences or
// prose. If the model returns nothing usable, RefinePrompt degrades gracefully:
// it returns a refinement that echoes the original intent (so the UI can still
// proceed) rather than erroring — a refine pass should never block generation.
func RefinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog) (PromptRefinement, error) {
	return refinePrompt(ctx, llm, intent, catalog, false)
}

// LightRefinePrompt runs a LIGHT touch-up pass for an already-refined,
// user-edited specification: it cleans up the text without the full rewrite, so
// re-generating after an edit is fast and faithful to what the user typed. Same
// output contract as RefinePrompt.
func LightRefinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog) (PromptRefinement, error) {
	return refinePrompt(ctx, llm, intent, catalog, true)
}

// refinePrompt is the shared implementation behind RefinePrompt (full) and
// LightRefinePrompt (touch-up). The only difference is which instruction the
// model is given; parsing, mode resolution, and graceful degradation are
// identical so both paths return the same PromptRefinement contract.
func refinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog, light bool) (PromptRefinement, error) {
	if strings.TrimSpace(intent) == "" {
		return PromptRefinement{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return PromptRefinement{}, fmt.Errorf("studio: no LLM configured")
	}

	var prompt string
	if light {
		prompt = BuildLightRefineInstruction(intent, catalog)
	} else {
		prompt = BuildRefinePromptInstruction(intent, catalog)
	}
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return PromptRefinement{}, fmt.Errorf("studio: llm complete: %w", err)
	}

	payload, perr := parseRefinement(raw)
	if perr != nil || strings.TrimSpace(payload.RefinedIntent) == "" {
		// Graceful degradation: never block generation on a bad refine, but do
		// not echo the rough prompt back to the user. Refine's UI contract is a
		// build-ready specification, so the local planner expands the raw intent
		// into the same sections Studio expects even when the model is unhelpful.
		return buildDeterministicRefinement(intent, catalog, light), nil
	}

	combined := payload.RefinedIntent + " " + intent
	// The refiner describes the request; it does not choose runtime architecture.
	// Treating its recommended_mode as an explicit operator selection let a model
	// label a detailed conversational spec "workflow", which the experimental
	// workflow guard then converted blindly to Plan-Execute. The same weather bot
	// that should use one native tool call per message consequently entered the
	// heavier planner and failed before execution. Keep architecture deterministic
	// and identical to the compile route: only the user's words may select ReAct,
	// while conversational assistants resolve to Auto and true multi-phase jobs
	// resolve to Plan-Execute.
	mode := RecommendAgentMode(combined)
	if mode == "" {
		mode = "auto"
	}
	refined := strings.TrimSpace(payload.RefinedIntent)
	if !light && refinementNeedsLocalExpansion(intent, refined) {
		local := buildDeterministicRefinement(intent, catalog, false)
		if strings.TrimSpace(payload.Summary) != "" {
			local.Summary = strings.TrimSpace(payload.Summary)
		}
		local.Assumptions = mergeStrings(local.Assumptions, trimStrings(payload.Assumptions))
		if len(payload.Questions) > 0 {
			local.Questions = payload.Questions
		}
		local.RecommendedMode = mode
		if strings.TrimSpace(payload.ModeReason) != "" {
			local.ModeReason = strings.TrimSpace(payload.ModeReason)
		}
		return local, nil
	}
	return PromptRefinement{
		Original:        intent,
		RefinedIntent:   refined,
		Summary:         strings.TrimSpace(payload.Summary),
		Assumptions:     trimStrings(payload.Assumptions),
		Questions:       payload.Questions,
		RecommendedMode: mode,
		ModeReason:      strings.TrimSpace(payload.ModeReason),
	}, nil
}

// buildDeterministicRefinement is Refine's local safety net. The model is still
// allowed to improve wording, but Studio owns the shape: a refined prompt must be
// a self-contained operating spec with trigger, inputs, steps, outputs, and
// edge cases. This prevents the prompt editor from showing an unchanged copy of
// the original when a local/compact model echoes the request.
func buildDeterministicRefinement(intent string, catalog Catalog, light bool) PromptRefinement {
	intent = strings.TrimSpace(intent)
	advice := AdviseStrategy(intent, catalog, "", false)
	mode := advice.Mode
	if mode == "" {
		mode = "auto"
	}
	sections := []string{
		"Goal\n" + firstSentence(intent),
		"Recommended architecture\n" + architectureSentence(advice),
		"Trigger\n" + refinedTrigger(intent),
		"Inputs and data sources\n" + refinedInputs(intent, catalog),
		"Processing steps\n" + refinedSteps(intent, catalog, advice),
		"Outputs and delivery\n" + refinedOutput(intent, catalog),
		"Edge cases and failure handling\n" + refinedEdgeCases(intent, advice),
	}
	spec := strings.Join(sections, "\n\n")
	if light {
		spec = strings.TrimSpace(intent)
	}
	return PromptRefinement{
		Original:        intent,
		RefinedIntent:   spec,
		Summary:         refinedSummary(intent, advice),
		Assumptions:     refinedAssumptions(intent, catalog, advice),
		Questions:       refinedQuestions(intent, catalog),
		RecommendedMode: mode,
		ModeReason:      advice.Reason,
	}
}

func refinementNeedsLocalExpansion(original, refined string) bool {
	o := normalizeRefineText(original)
	r := normalizeRefineText(refined)
	if r == "" {
		return true
	}
	if o == r {
		return true
	}
	// If most of the original text is returned and the Studio sections are still
	// missing, treat it as an echo even if the model added a small preface.
	if len(o) > 80 && strings.Contains(r, o[:minInt(len(o), 80)]) && countSpecSections(refined) < 3 {
		return true
	}
	return false
}

func normalizeRefineText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func countSpecSections(s string) int {
	t := strings.ToLower(s)
	n := 0
	for _, marker := range []string{
		"goal", "recommended architecture", "trigger", "inputs", "data sources",
		"processing steps", "outputs", "delivery", "edge cases", "failure handling",
	} {
		if strings.Contains(t, marker) {
			n++
		}
	}
	return n
}

func architectureSentence(advice StrategyAdvice) string {
	switch advice.Mode {
	case "workflow":
		pattern := strings.TrimSpace(advice.DeterministicPattern)
		if pattern == "" {
			pattern = "fixed pipeline"
		}
		return "Build this as a deterministic Workflow. Soulacy should own the graph and run the same bounded steps each time. Pattern: " + pattern + "."
	case "plan_execute":
		return "Build this as a Plan-Execute agent. The agent should make an explicit plan, execute bounded steps, and stop with a useful result."
	case "react":
		return "Build this as an explicit ReAct agent only because the user asked for that strategy."
	default:
		return "Build this as an Auto agent. The runtime may use native tool calling, but Studio should still provide a clear operating prompt."
	}
}

func refinedTrigger(intent string) string {
	li := strings.ToLower(intent)
	switch {
	case containsAny(li, "weekday", "weekdays", "monday", "tuesday", "wednesday", "thursday", "friday"):
		if strings.Contains(li, "7:00") || strings.Contains(li, "7am") || strings.Contains(li, "7 am") {
			return "Run automatically every weekday at 7:00 AM local time."
		}
		return "Run automatically every weekday at the requested local time."
	case containsAny(li, "daily", "every morning", "every day"):
		return "Run automatically once per day at the requested local time."
	case containsAny(li, "cron", "schedule", "scheduled"):
		return "Run on the schedule described by the user."
	case containsAny(li, "incoming message", "telegram", "slack", "discord", "chat"):
		return "Run when an incoming user/channel message is routed to this agent."
	case containsAny(li, "webhook", "http"):
		return "Run when an inbound webhook request is received."
	default:
		return "Run manually unless the user adds a schedule or channel trigger."
	}
}

func refinedInputs(intent string, catalog Catalog) string {
	var parts []string
	if domains := extractDomains(intent); len(domains) > 0 {
		parts = append(parts, "Use these web sources: "+strings.Join(domains, ", ")+".")
	}
	if containsAny(strings.ToLower(intent), "cookie", "cookies.txt", "paywall", "paywalled") {
		parts = append(parts, "For paywalled pages, read the matching Netscape cookies file from ~/.soulacy/soulspace/<domain>_cookies.txt and fetch with a cookie-aware Custom Python step. Do not use fetch_url for sources that require cookies.")
	}
	if containsAny(strings.ToLower(intent), "notebooklm", "notebook lm") {
		parts = append(parts, "Use the configured NotebookLM MCP tools for notebook creation, source ingestion, studio artifact creation, status polling, and artifact download/link retrieval.")
	}
	if containsAny(strings.ToLower(intent), "document", "documents", "file", "url", "urls") {
		parts = append(parts, "Accept user-provided URLs and uploaded documents as primary artifacts.")
	}
	if len(catalog.KnowledgeBases) > 0 && containsAny(strings.ToLower(intent), "knowledge", "kb", "store") {
		parts = append(parts, "Store searchable artifacts in the matching configured knowledge base.")
	}
	if len(parts) == 0 {
		parts = append(parts, "Use the user's message and only the tools, skills, MCP servers, channels, and knowledge bases exposed in Studio's catalog.")
	}
	return strings.Join(parts, "\n")
}

func refinedSteps(intent string, catalog Catalog, advice StrategyAdvice) string {
	if deterministicNotebookPodcastWorkflow(intent) {
		return strings.Join([]string{
			"1. Search the requested sources for candidate AI articles.",
			"2. Fetch each candidate with the correct method: cookie-aware Custom Python for paywalled domains, normal web fetch only for public pages.",
			"3. Validate that each fetched page is a real, recent AI article; discard failed, duplicate, stale, or irrelevant items.",
			"4. Create a NotebookLM notebook for the briefing and capture the notebook_id.",
			"5. Add each validated article URL/source to the same notebook_id.",
			"6. Request a NotebookLM audio overview/podcast artifact.",
			"7. Poll status until the artifact is complete or the run reaches its timeout.",
			"8. Deliver the episode title, audio link, and included article list to the selected output channel.",
		}, "\n")
	}
	if knowledgeIngestionWorkflow(intent) {
		return strings.Join([]string{
			"1. Parse the incoming URL/document artifact.",
			"2. Fetch or extract readable text safely without loading unbounded content into memory.",
			"3. Ask the LLM only to summarize/classify/tag the extracted content.",
			"4. Write the tagged artifact into the selected knowledge base using the knowledge-store tool, not shell commands.",
			"5. Return a concise confirmation with title, tags, and storage location.",
		}, "\n")
	}
	if researchDigestWorkflow(intent) || dealDigestWorkflow(intent) || stockDigestWorkflow(intent) {
		return strings.Join([]string{
			"1. Collect candidate results from the requested sources.",
			"2. Filter for relevance, freshness, duplicates, and user constraints.",
			"3. Summarize the highest-value findings in a concise digest.",
			"4. Deliver the digest to the selected channel or return it in chat.",
		}, "\n")
	}
	if advice.Mode == "plan_execute" {
		return "1. Build a short execution plan from the user's request.\n2. Execute each plan step using only approved tools.\n3. Recover from tool errors with bounded retries or a clear partial result.\n4. Stop once the user-facing objective is complete."
	}
	if advice.Mode == "auto" {
		return "1. Interpret the user's request.\n2. Choose the most relevant approved tool or skill.\n3. Call tools only when needed.\n4. Answer concisely with sources, artifacts, or next actions when available."
	}
	return "1. Follow the user's requested steps in order.\n2. Validate each intermediate output before passing it downstream.\n3. Produce the final result through the selected output path."
}

func refinedOutput(intent string, catalog Catalog) string {
	li := strings.ToLower(intent)
	var outs []string
	for _, ch := range catalog.Channels {
		if strings.Contains(li, strings.ToLower(ch)) {
			outs = append(outs, ch)
		}
	}
	for _, ch := range []string{"telegram", "slack", "discord", "email", "http"} {
		if strings.Contains(li, ch) && !containsStringFold(outs, ch) {
			outs = append(outs, ch)
		}
	}
	if len(outs) == 0 {
		return "Return the result in chat unless the user selects an output channel before saving."
	}
	return "Deliver the final result to: " + strings.Join(outs, ", ") + ". Do not expose raw JSON or chart blocks to chat apps unless that channel supports rendering them."
}

func refinedEdgeCases(intent string, advice StrategyAdvice) string {
	var cases []string
	cases = append(cases, "If no useful data is found, send a short no-results message instead of failing silently.")
	cases = append(cases, "If a required external service, credential, channel, or MCP server is missing, stop with an actionable setup message.")
	if containsAny(strings.ToLower(intent), "paywall", "paywalled", "cookie") {
		cases = append(cases, "If cookie-based fetch fails with 401/403, mark that source as unavailable and continue with other sources.")
	}
	if advice.Mode == "workflow" {
		cases = append(cases, "Keep loops bounded and make every branch converge to a final output or an explicit halt.")
	}
	return strings.Join(cases, "\n")
}

func refinedSummary(intent string, advice StrategyAdvice) string {
	switch advice.Mode {
	case "workflow":
		return "A deterministic workflow will run the requested automation with explicit inputs, bounded steps, and configured delivery."
	case "plan_execute":
		return "A Plan-Execute agent will plan and complete the requested multi-step job using approved tools."
	case "react":
		return "An explicit ReAct agent will run a think-act-observe loop as requested."
	default:
		return "An Auto agent will interpret user requests and use approved tools interactively."
	}
}

func refinedAssumptions(intent string, catalog Catalog, advice StrategyAdvice) []string {
	var out []string
	if !containsAny(strings.ToLower(intent), "telegram", "slack", "discord", "email", "http") {
		out = append(out, "Assumed results should be returned in chat until an output channel is selected.")
	}
	if advice.Mode == "workflow" && !containsAny(strings.ToLower(intent), "schedule", "daily", "weekday", "manual", "webhook", "incoming") {
		out = append(out, "Assumed manual trigger because no schedule or inbound channel trigger was specified.")
	}
	if len(catalog.Channels) == 0 && containsAny(strings.ToLower(intent), "telegram", "slack", "discord", "email") {
		out = append(out, "Assumed the named delivery channel will be configured before saving or running live.")
	}
	return out
}

func refinedQuestions(intent string, catalog Catalog) []Question {
	li := strings.ToLower(intent)
	if containsAny(li, "telegram", "slack", "discord", "email") && len(catalog.Channels) == 0 {
		return []Question{{
			ID:      "delivery_channel",
			Text:    "Which configured output channel should receive the final result?",
			Options: []string{"telegram", "slack", "email"},
		}}
	}
	return nil
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Build the requested Soulacy agent."
	}
	for _, sep := range []string{"\n", ". "} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i+len(strings.TrimSpace(sep))])
		}
	}
	return s
}

func extractDomains(s string) []string {
	re := regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b`)
	raw := re.FindAllString(s, -1)
	seen := map[string]bool{}
	var out []string
	for _, d := range raw {
		d = strings.ToLower(strings.Trim(d, ".,;:()[]{}<>\"'"))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(a, b...) {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func containsStringFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// normalizeMode canonicalizes a model-supplied mode to workflow|auto|react|
// plan_execute, or "" if unrecognized.
func normalizeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "workflow", "flow":
		return "workflow"
	case "auto", "tool", "tool_agent", "tool-agent", "native_tool", "native-tool":
		return "auto"
	case "react":
		return "react"
	case "plan_execute", "plan-execute", "planexecute":
		return "plan_execute"
	}
	return ""
}

// explicitReActRequested reports whether the user intentionally asked for the
// classic ReAct loop. Without this explicit request, Studio must not produce
// ReAct automatically; it should pick Auto or Plan-Execute instead.
func explicitReActRequested(intent string) bool {
	t := strings.ToLower(intent)
	cues := []string{
		"react", "re-act", "reasoning loop", "think-act-observe", "think act observe",
		"thought/action/observation", "thought action observation", "classic react",
		"force react", "explicit react",
	}
	for _, c := range cues {
		if mentionsUnnegated(t, c) {
			return true
		}
	}
	return false
}

// mentionsUnnegated reports whether phrase appears in t NOT immediately preceded
// by a negation ("not", "no", "never", "without"). So "use a react loop" counts
// as a react request while "not a react agent" does not — critical for prompts
// like "a fixed workflow (not a ReAct or Plan-Execute agent)".
func mentionsUnnegated(t, phrase string) bool {
	from := 0
	for {
		i := strings.Index(t[from:], phrase)
		if i < 0 {
			return false
		}
		i += from
		pre := t[maxInt(0, i-9):i]
		if !strings.Contains(pre, "not ") && !strings.Contains(pre, "no ") &&
			!strings.Contains(pre, "never ") && !strings.Contains(pre, "without ") {
			return true
		}
		from = i + len(phrase)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// hasStrongReasoningCues reports whether the intent has signals that a FIXED flow
// cannot satisfy (asynchronous jobs that must be polled, per-item loops, or
// driving an interactive multi-step external service like NotebookLM). These
// override a model's "workflow" guess — we've seen fixed flows fail every time
// on these. They now route to Plan-Execute unless the user explicitly asks for
// ReAct.
func hasStrongReasoningCues(intent string) bool {
	t := strings.ToLower(intent)
	strong := []string{
		// Async jobs / polling / per-item loops a fixed DAG physically can't do.
		"notebooklm", "notebook lm", "audio overview",
		"poll", "until ready", "until it is ready", "until complete", "until completed",
		"until done", "until it finishes", "wait until", "wait for it",
		"each source", "one at a time", "one by one", "for each ", "per article", "per item",
		"check status", "status until", "generation status",
		// Dynamic capability ROUTING: the agent must decide WHICH skill/tool to use
		// per request. This is the canonical reasoning pattern (an interactive
		// assistant that maps an arbitrary question to the right skill), and a fixed
		// graph can't branch over open-ended input — it's always an agent.
		"appropriate skill", "appropriate skills", "best-matching skill", "right skill",
		"selects and calls", "select the skill", "selects the skill", "choose the skill",
		"maps the question", "map the question", "route to the", "routes to the",
		"based on the parsed intent", "based on the question", "depending on the question",
		"which skill", "which tool", "selects the appropriate", "select the appropriate",
		"on-demand", "answers questions", "responds to questions", "responds to user questions",
		"responds to incoming questions", "natural-language question",
	}
	for _, c := range strong {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// hasStrongReactCues is kept as a compatibility alias for older tests/helpers.
func hasStrongReactCues(intent string) bool {
	return hasStrongReasoningCues(intent)
}

// hasPlanExecuteCues reports whether the intent describes an explicitly
// MULTI-PHASE job that benefits from planning the whole sequence before acting —
// the Plan-Execute pattern. Checked before the ReAct cues because a long
// decompose-then-run task is more specific than generic dynamic routing.
func hasPlanExecuteCues(intent string) bool {
	t := strings.ToLower(intent)
	cues := []string{
		"plan and execute", "plan-execute", "plan then execute",
		"decompose", "break it down into", "break this down into", "break down the task",
		"multi-step plan", "multistep plan", "step-by-step plan", "create a plan",
		"first plan", "outline the steps then", "devise a plan", "research plan",
	}
	for _, c := range cues {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// RecommendAgentMode is the SINGLE authoritative default architecture decision.
// It returns "auto", "plan_execute", or "react" from the intent text. Workflow
// is absent by design: fixed-graph generation requires the separate explicit
// experimental opt-in. ReAct is advanced/manual and is selected only when the
// user explicitly asks for it.
func RecommendAgentMode(intent string) string {
	advice := AdviseStrategy(intent, Catalog{}, "", false)
	if advice.Mode == "workflow" {
		return ""
	}
	if advice.RuntimeStrategy != "" {
		return advice.RuntimeStrategy
	}
	return advice.Mode
}

// explicitWorkflowRequested reports whether the user explicitly asked for a
// fixed workflow (and did NOT ask for ReAct, which is handled first). Kept
// specific so a passing mention of the word "workflow" in a reasoning task
// doesn't misroute — it looks for a clear "build as a fixed workflow" style cue.
func explicitWorkflowRequested(intent string) bool {
	if structuredWorkflowProcedureRequested(intent) {
		return true
	}
	return explicitWorkflowPhrase(intent)
}

// explicitWorkflowPhrase is the STRONG half of explicitWorkflowRequested: the
// user said, in words, that they want a fixed workflow.
//
// Split out from the structural inference because the two carry very different
// weight. "Build this as a fixed workflow" is a decision. A numbered spec is
// only formatting — and Studio's own refiner PRODUCES numbered specs, rewriting
// "a conversational travel agent" into "1. TRIGGER: … 2. INPUTS: …". Reading
// that back as an explicit request meant Studio's formatting of the user's words
// outvoted the words themselves.
func explicitWorkflowPhrase(intent string) bool {
	t := strings.ToLower(intent)
	// Positive "build a fixed workflow" phrasings — must be UNnegated so
	// "not a fixed workflow" (a react request) doesn't count as a workflow one.
	for _, c := range []string{
		"fixed workflow", "fixed flow", "as a workflow", "workflow strategy",
		"deterministic workflow", "force workflow",
	} {
		if mentionsUnnegated(t, c) {
			return true
		}
	}
	// Explicit "not an agent" phrasings are themselves a positive workflow signal.
	for _, c := range []string{
		"not a react", "not a reasoning agent", "not a plan execute", "not a plan-execute",
		"not a plan_execute", "not an agent",
	} {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// structuredWorkflowProcedureRequested detects prompts where the user has
// already specified the workflow topology as an ordered operating procedure.
// These prompts may mention loops, polling, or NotebookLM, but the human intent
// is still a deterministic graph with bounded branches, not an adaptive agent.
func structuredWorkflowProcedureRequested(intent string) bool {
	t := strings.ToLower(intent)
	if strings.TrimSpace(t) == "" {
		return false
	}

	numbered := 0
	for i := 1; i <= 15; i++ {
		if strings.Contains(t, fmt.Sprintf("%d.", i)) || strings.Contains(t, fmt.Sprintf("%d)", i)) {
			numbered++
		}
	}

	labels := 0
	for _, label := range []string{
		"trigger:", "search:", "fetch", "fetch & validate:", "validate:",
		"aggregate", "aggregate & check:", "check:", "create notebook:",
		"add sources:", "generate audio:", "poll status:", "deliver output:",
		"deliver:", "output:",
	} {
		if strings.Contains(t, label) {
			labels++
		}
	}

	scheduleCue := containsAny(t,
		"schedule", "scheduled", "weekday", "daily", "every day", "every morning",
		"run automatically", "cron", "7:00", "7am", "7 am",
	)
	deterministicCue := containsAny(t,
		"if the list is empty", "halt execution", "discard", "send a telegram",
		"capture the resulting", "passing the same", "loop through", "for each candidate",
		"for each url", "single list", "final podcast",
	)

	if numbered >= 5 {
		return true
	}
	if numbered >= 3 && (labels >= 2 || (scheduleCue && deterministicCue)) {
		return true
	}
	if labels >= 5 && scheduleCue {
		return true
	}
	return false
}

// parseRefinement tolerantly extracts the refine JSON from raw model output,
// reusing the same fence-stripping + outermost-object narrowing as ParseDraft.
func parseRefinement(raw string) (refinePromptPayload, error) {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return refinePromptPayload{}, fmt.Errorf("studio: no JSON object found in refine output")
	}
	s = s[start : end+1]
	var p refinePromptPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return refinePromptPayload{}, fmt.Errorf("studio: parse refine: %w", err)
	}
	return p, nil
}

// trimStrings trims each entry and drops empties, keeping the assumptions list
// clean for the UI.
func trimStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
