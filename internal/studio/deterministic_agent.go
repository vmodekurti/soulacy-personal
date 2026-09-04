package studio

import (
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/agentprompt"
)

// CompileDeterministicAgent builds Studio's default agent form without asking
// the LLM to design architecture or emit JSON. The LLM may still refine the
// user's words before this function is called; Soulacy owns strategy choice,
// trigger inference, capability selection, and the system-prompt scaffold.
func CompileDeterministicAgent(intent string, cat Catalog, strategy string, answers map[string]string) (Result, bool) {
	intent = strings.TrimSpace(intent)
	if intent == "" {
		return Result{}, false
	}
	strategy = normalizeDeterministicStrategy(intent, strategy)
	draft := Draft{
		Name:         deterministicAgentName(intent),
		Intent:       intent,
		RawIntent:    strings.TrimSpace(cat.RawIntent),
		Trigger:      inferredTriggerFromIntent(intent),
		Strategy:     strategy,
		Tools:        deterministicTools(intent, cat),
		Knowledge:    deterministicKnowledge(intent, cat),
		Channels:     deterministicChannels(intent, cat),
		StepTimeout:  "120s",
		TotalTimeout: "900s",
		MaxSteps:     deterministicMaxSteps(intent),
		MaxTurns:     15,
	}
	normalizeTrigger(&draft, intent)
	if cron, ok := draft.Trigger.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
		draft.Unattended = true
	}
	if len(draft.Tools) == 0 {
		return Result{}, false
	}
	draft.SystemPrompt = deterministicSystemPrompt(draft, intent, answers, cat.Lessons)
	draft.SystemPrompt = agentprompt.EnsureShared(draft.SystemPrompt)
	draft.Recommendation = &Recommendation{
		Mode:      strategy,
		Rationale: deterministicStrategyReason(intent, strategy),
	}

	originalSkills := append([]string(nil), draft.Skills...)
	notes := []string{"Studio used Soulacy's deterministic planner: the framework selected strategy, trigger, tools, and guardrails; the LLM only refined the prompt text."}
	notes = append(notes, GroundAgentCapabilities(&draft, cat)...)
	notes = append(notes, applyGenerationDefaults(&draft, intent)...)
	if len(draft.Tools) == 0 {
		return Result{}, false
	}
	res := Result{
		Workflow:    draft,
		Notes:       notes,
		Suggestions: append(suggestMissingAgent(draft, cat), MissingSkillSuggestions(originalSkills, cat)...),
		Plan:        BuildPlan(intent, cat),
		Generation:  cat.Generation,
	}
	exp := ExplainDraft(draft)
	res.Explanation = &exp
	return res, true
}

func normalizeDeterministicStrategy(intent, requested string) string {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "plan_execute" || req == "auto" {
		return req
	}
	// ReAct remains available only when explicitly requested. Studio-generated
	// agents otherwise use native Auto or Plan-Execute, which are less brittle
	// with frontier models and safer with compact local models.
	if req == "react" && explicitReActRequested(intent) {
		return "react"
	}
	li := strings.ToLower(intent)
	if strings.Contains(li, "podcast") || strings.Contains(li, "notebooklm") || strings.Contains(li, "notebook lm") ||
		strings.Contains(li, "daily") || strings.Contains(li, "every morning") || strings.Contains(li, "schedule") ||
		strings.Contains(li, "process") || strings.Contains(li, "workflow") {
		return "plan_execute"
	}
	return "auto"
}

func deterministicStrategyReason(intent, strategy string) string {
	switch strategy {
	case "plan_execute":
		return "Soulacy selected a planned tool-agent because the task has multiple phases, scheduled work, polling, or external side effects that should be executed with an explicit plan."
	case "react":
		return "The user explicitly requested a ReAct-style reasoning loop."
	default:
		return "Soulacy selected Auto so capable models can use native tool calling, while the runtime can fall back to a safer loop when needed."
	}
}

func deterministicMaxSteps(intent string) int {
	li := strings.ToLower(intent)
	if strings.Contains(li, "podcast") || strings.Contains(li, "notebook") || strings.Contains(li, "daily") || strings.Contains(li, "schedule") {
		return 30
	}
	if strings.Contains(li, "research") || strings.Contains(li, "stock") || strings.Contains(li, "deal") || strings.Contains(li, "article") {
		return 24
	}
	return 18
}

// deterministicAgentName picks a display name from the intent.
//
// Matching is on WHOLE WORDS, and the finance case needs corroboration.
// Substring matching named a travel advisor "Stock Advisor", because
// "flight/hotel options" contains "option" — the same collision that let a
// keyword pattern claim the same prompt for a market digest. One ambiguous
// word should not outweigh a paragraph about travel.
//
// Ordered most-specific first: a prompt can mention several domains, and the
// one it is actually ABOUT is usually the one named earliest and most often.
func deterministicAgentName(intent string) string {
	li := strings.ToLower(intent)
	word := func(words ...string) bool {
		for _, w := range words {
			if containsWord(li, w) {
				return true
			}
		}
		return false
	}
	switch {
	case word("weather", "forecast"):
		return "Weather Expert"
	case word("notebook", "notebooklm", "podcast"):
		return "Notebook Podcast Agent"
	case word("travel", "flight", "flights", "hotel", "hotels", "itinerary", "itineraries", "trip", "destination", "destinations"):
		return "Travel Advisor"
	case word("knowledge", "kb", "document", "documents", "url", "urls"):
		return "Knowledge Ingestion Agent"
	// "options" alone is ambiguous — it is as likely to be flight options as
	// stock options — so it only counts alongside an unambiguous finance word.
	case word("stock", "stocks", "ticker", "equity", "equities", "portfolio", "earnings", "finance", "financial"),
		word("option", "options") && word("strike", "expiry", "call", "put", "trading", "market"):
		return "Stock Advisor"
	case word("deal", "deals", "discount", "discounts", "coupon", "coupons"):
		return "Deal Finder"
	case word("research"):
		return "Research Agent"
	default:
		return "Soulacy Agent"
	}
}

// containsWord reports a WHOLE-word match, so "option" does not fire inside
// "options", "optional" or "exception".
func containsWord(text, word string) bool {
	if word == "" {
		return false
	}
	for i := 0; i <= len(text)-len(word); {
		j := strings.Index(text[i:], word)
		if j < 0 {
			return false
		}
		j += i
		startOK := j == 0 || !isWordByte(text[j-1])
		end := j + len(word)
		endOK := end == len(text) || !isWordByte(text[end])
		if startOK && endOK {
			return true
		}
		i = j + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

func deterministicChannels(intent string, cat Catalog) []string {
	li := strings.ToLower(intent)
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || !catalogHasChannel(cat, name) || containsFold(out, name) {
			return
		}
		out = append(out, name)
	}
	for _, ch := range []string{"telegram", "slack", "discord", "whatsapp", "http"} {
		if strings.Contains(li, ch) {
			add(ch)
		}
	}
	if len(out) == 0 && (strings.Contains(li, "send") || strings.Contains(li, "notify") || strings.Contains(li, "deliver")) {
		for _, ch := range cat.Channels {
			if !strings.EqualFold(ch, "http") {
				add(ch)
				break
			}
		}
	}
	return out
}

func catalogHasChannel(cat Catalog, name string) bool {
	if len(cat.Channels) == 0 {
		return true
	}
	for _, ch := range cat.Channels {
		if strings.EqualFold(strings.TrimSpace(ch), name) {
			return true
		}
	}
	return false
}

func deterministicKnowledge(intent string, cat Catalog) []string {
	if len(cat.KnowledgeBases) == 0 {
		return nil
	}
	li := strings.ToLower(intent)
	var out []string
	for _, kb := range cat.KnowledgeBases {
		name := strings.TrimSpace(kb.Name)
		if name == "" {
			continue
		}
		text := strings.ToLower(name + " " + kb.Description)
		if strings.Contains(li, strings.ToLower(name)) || sharedDistinctTokens(li, text) >= 2 ||
			(strings.Contains(li, "knowledge") || strings.Contains(li, "kb") || strings.Contains(li, "document")) {
			out = append(out, name)
		}
		if len(out) >= 3 {
			break
		}
	}
	return uniqueStrings(out)
}

func deterministicTools(intent string, cat Catalog) []string {
	li := strings.ToLower(intent)
	var out []string
	addBuiltin := func(name string) {
		if hasCatalogBuiltin(cat, name) {
			out = append(out, name)
		}
	}
	addMCP := func(hints ...string) {
		out = append(out, matchingMCPTools(cat, hints...)...)
	}

	needsSearch := anyContains(li, "search", "research", "find", "latest", "news", "deal", "stock", "option", "article", "url", "web")
	// Tool names such as mcp__weather__search_location contain "search" but do
	// not request a generic web search. Prefer the named, installed MCP capability
	// unless the operator separately asked to search the web.
	if strings.Contains(li, "mcp__weather__") && !anyContains(li, "web search", "search the web", "internet search") {
		needsSearch = false
	}
	needsFetch := anyContains(li, "url", "article", "document", "page", "fetch", "scrape", "ingest")
	needsKB := anyContains(li, "knowledge", "kb", "store", "ingest", "tag", "document")
	needsQueue := anyContains(li, "queue", "daily", "schedule", "later", "pending", "inbox")
	sameChannelReply := sameChannelReplyIntent(intent)
	needsDelivery := !sameChannelReply && anyContains(li, "send", "notify", "telegram", "slack", "discord", "whatsapp", "deliver")

	if needsSearch {
		addBuiltin("web_search")
	}
	if needsFetch {
		addBuiltin("fetch_url")
		addBuiltin("http_request")
	}
	if needsKB {
		addBuiltin("kb_write")
		addBuiltin("kb_search")
	}
	if needsQueue {
		addBuiltin("queue_put")
		addBuiltin("queue_take")
		addBuiltin("queue_list")
		addBuiltin("queue_names")
	}
	if needsDelivery || (len(deterministicChannels(intent, cat)) > 0 && !sameChannelReply) {
		addBuiltin("channel.send")
		addBuiltin("channel.status")
	}

	switch {
	case anyContains(li, "notebooklm", "notebook lm", "podcast", "audio overview", "audio summary"):
		addMCP("notebooklm", "notebook", "source", "audio", "studio", "status", "refresh", "auth")
		addBuiltin("fetch_url")
		addBuiltin("channel.send")
		addBuiltin("channel.status")
	case strings.Contains(li, "weather"):
		weatherTools := matchingMCPTools(cat, "weather", "forecast", "current", "location", "resolve", "alert", "openmeteo", "open-meteo")
		out = append(out, weatherTools...)
		if len(weatherTools) == 0 {
			addBuiltin("web_search")
		}
	case anyContains(li, "stock", "option", "finance", "ticker", "earnings", "market"):
		addMCP("yahoo", "finance", "stock", "quote", "ticker", "option", "earnings", "funda")
		addBuiltin("web_search")
	case anyContains(li, "browser", "website", "click", "login"):
		addMCP("browser", "chrome", "playwright", "navigate", "screenshot")
	}

	// Tools the user named outright always win a place. This runs AFTER the topic
	// switch so an explicit mention is never lost to a domain case that happened
	// not to cover it — naming a tool in the prompt is the least ambiguous signal
	// available, and ignoring it was the bug this closes.
	out = append(out, namedMCPTools(intent, cat)...)

	if len(out) == 0 {
		addBuiltin("web_search")
		addBuiltin("fetch_url")
	}
	return uniqueStrings(out)
}

// sameChannelReplyIntent distinguishes an ordinary reply from an outbound
// delivery action. The runtime automatically sends an agent's final answer back
// over the inbound channel; granting channel.send for wording such as "respond
// via the same channel" adds a privileged side effect, confirmation gate, and
// intent-deny policy that the conversation neither needs nor requested.
func sameChannelReplyIntent(intent string) bool {
	li := strings.ToLower(intent)
	if !ConversationalIntent(intent) {
		return false
	}
	return anyContains(li,
		"same channel", "channel the query arrived", "channel the message arrived",
		"sends it back to the user", "send it back to the user",
		"responds to the user", "respond to the user", "replies to the user",
		"reply to the user", "returns the answer to the user")
}

func hasCatalogBuiltin(cat Catalog, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || !isBuiltinContractTool(name) {
		return false
	}
	if len(cat.Tools) == 0 {
		return true
	}
	for _, t := range cat.Tools {
		if strings.EqualFold(strings.TrimSpace(t), name) {
			return true
		}
	}
	return false
}

// namedMCPTools returns the MCP tools the intent EXPLICITLY names, matched
// against the catalogue rather than against a hardcoded topic list.
//
// deterministicTools routes MCP selection through a switch over known domains —
// notebooklm, weather, finance, browser. Anything else got nothing, so a prompt
// saying in plain words "the agent uses the travel MCP tool" produced an agent
// with no travel tool: the server was installed, the catalogue had it, and the
// planner had no case for it. Adding a "travel" case would fix that one prompt
// and fail for the next server someone installs.
//
// So this matches on what is actually there: a server id or a tool name that
// appears in the intent. It is deliberately conservative — a bare word must
// match a real catalogue entry, so it can only ever surface tools this
// workspace has.
func namedMCPTools(intent string, cat Catalog) []string {
	li := strings.ToLower(intent)
	var out []string
	for _, srv := range cat.MCP {
		server := strings.ToLower(strings.TrimSpace(srv.Server))
		// A server named in the intent contributes all of its tools: the user
		// referred to the capability, not to one specific entry point.
		serverNamed := server != "" && len(server) >= 3 && strings.Contains(li, server)
		for _, tool := range srv.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			if serverNamed {
				out = append(out, name)
				continue
			}
			// Otherwise the tool itself has to be named. Compare on the bare tool
			// word too, since a user writes "the travel tool", not
			// "mcp__trvl__travel".
			bare := strings.ToLower(name)
			if i := strings.LastIndex(bare, "__"); i >= 0 {
				bare = bare[i+2:]
			}
			if len(bare) >= 3 && strings.Contains(li, bare) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

func matchingMCPTools(cat Catalog, hints ...string) []string {
	var out []string
	for _, srv := range cat.MCP {
		server := strings.ToLower(srv.Server)
		for _, tool := range srv.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			text := strings.ToLower(server + " " + name + " " + tool.Description)
			score := 0
			for _, hint := range hints {
				hint = strings.ToLower(strings.TrimSpace(hint))
				if hint != "" && strings.Contains(text, hint) {
					score++
				}
			}
			if score > 0 {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	if len(out) > 16 {
		out = out[:16]
	}
	return uniqueStrings(out)
}

func deterministicSystemPrompt(d Draft, intent string, answers map[string]string, lessons []Lesson) string {
	var sb strings.Builder
	sb.WriteString("You are ")
	sb.WriteString(d.Name)
	sb.WriteString(", a Soulacy agent built by Studio's deterministic planner.\n\n")
	sb.WriteString("Mission:\n")
	sb.WriteString("- ")
	sb.WriteString(intent)
	sb.WriteString("\n")
	if len(answers) > 0 {
		sb.WriteString("- User clarifications: ")
		var keys []string
		for k := range answers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(strings.TrimSpace(answers[k]))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nExecution rules:\n")
	sb.WriteString("- Use the provided tools directly; do not invent tool names, arguments, files, variables, or APIs.\n")
	sb.WriteString("- Prefer native tool calls when available. If a tool fails, inspect the error once, correct arguments if obvious, and continue with a useful fallback.\n")
	sb.WriteString("- Never call shell/system tools unless the agent explicitly has that capability and the user approved the action.\n")
	sb.WriteString("- Do not use channel.send for ordinary chat replies. Use channel.send only for scheduled/outbound delivery, and pass channel, to when known, and text.\n")
	sb.WriteString("- If a result is partial, clearly say what succeeded, what failed, and the next concrete fix.\n")
	sb.WriteString("- Final answers must be human-readable Markdown, not raw JSON, unless the user explicitly asks for JSON.\n")
	sb.WriteString("- For charts, include a compact fenced chart block only when numeric series data exists.\n")
	if block := strings.TrimSpace(LessonsPromptBlock(lessons)); block != "" {
		sb.WriteString("\n")
		sb.WriteString(block)
		sb.WriteString("\n")
	}

	li := strings.ToLower(intent)
	switch {
	case anyContains(li, "notebooklm", "notebook lm", "podcast", "audio overview", "audio summary"):
		sb.WriteString("\nNotebookLM playbook:\n")
		sb.WriteString("1. Refresh/auth-check NotebookLM if that tool exists.\n")
		sb.WriteString("2. Create or select a notebook before adding sources; preserve the notebook id from the tool result.\n")
		sb.WriteString("3. Add each URL/document source to that notebook. Do not generate audio before sources are attached.\n")
		sb.WriteString("4. Generate the audio overview/podcast and poll status when a status tool exists.\n")
		sb.WriteString("5. Return the podcast link or a precise failure reason; never claim success without an artifact or confirmed status.\n")
	case anyContains(li, "knowledge", "kb", "document", "url", "ingest", "tag"):
		sb.WriteString("\nKnowledge ingestion playbook:\n")
		sb.WriteString("1. Detect whether the input is a URL, document, or plain text.\n")
		sb.WriteString("2. Fetch/read content with bounded size and summarize before storing large artifacts.\n")
		sb.WriteString("3. Generate 3-8 useful tags from the actual content, then write to the selected KB with kb_write.\n")
		sb.WriteString("4. Verify storage with kb_search when available and report the KB name, title, source, tags, and any skipped items.\n")
	case strings.Contains(li, "weather"):
		sb.WriteString("\nWeather playbook:\n")
		sb.WriteString("1. Extract only the location and user intent before calling weather/location tools.\n")
		sb.WriteString("2. Use current conditions for now/today; forecast for planning; alerts for safety risk.\n")
		sb.WriteString("3. Give direct answer, decision guidance, best/risk window, key conditions, confidence, and safety notes.\n")
	case anyContains(li, "stock", "option", "finance", "ticker", "market"):
		sb.WriteString("\nMarket analysis playbook:\n")
		sb.WriteString("1. Resolve ticker symbols explicitly and pass required ticker/symbol fields exactly as tool schemas expect.\n")
		sb.WriteString("2. Combine market data, news, fundamentals, sentiment, and risk; label missing data instead of inventing it.\n")
		sb.WriteString("3. Give a practical conclusion with assumptions, risks, and time horizon. This is not financial advice.\n")
	case anyContains(li, "deal", "discount", "price"):
		sb.WriteString("\nDeal-finding playbook:\n")
		sb.WriteString("1. Search multiple relevant sources with concrete product terms.\n")
		sb.WriteString("2. Dedupe by URL/product, compare price/shipping/condition, and explain why the top pick wins.\n")
		sb.WriteString("3. If no credible deal is found, say so and suggest the best next search terms.\n")
	}
	return sb.String()
}

func anyContains(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func uniqueStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || seen[strings.ToLower(x)] {
			continue
		}
		seen[strings.ToLower(x)] = true
		out = append(out, x)
	}
	return out
}

func sharedDistinctTokens(a, b string) int {
	at := tokenize(a)
	bt := tokenize(b)
	n := 0
	for t := range at {
		if len(t) > 3 && bt[t] {
			n++
		}
	}
	return n
}
