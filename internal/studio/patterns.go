package studio

import (
	"sort"
	"strconv"
	"strings"
)

// Pattern is a curated, reusable workflow blueprint for a common automation
// (Story #15). It is NOT a literal flow that gets pasted in; it is guidance the
// compiler is grounded in so it produces the correct STEP ORDER and data flow
// for well-known jobs (e.g. NotebookLM podcast creation) instead of relearning
// them from scratch — and so it doesn't emit broken sequences like generating
// audio before a notebook exists.
type Pattern struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Keywords trigger the pattern when they appear in the intent (lowercased,
	// substring match).
	Keywords []string `json:"keywords"`
	// RequiresMCP, when set, names MCP servers (or capability hints) the pattern
	// depends on; used both to rank relevance and to warn when the capability is
	// absent.
	RequiresMCP []string `json:"requires_mcp,omitempty"`
	// Summary is a one-line description of what the pattern accomplishes.
	Summary string `json:"summary"`
	// Steps is the ordered sequence of high-level steps, written so the compiler
	// emits nodes in the right order with the right data dependencies. Phrase
	// each step as an instruction including the critical ordering/IO contract.
	Steps []string `json:"steps"`
	// Plan is the DETERMINISTIC skeleton: concrete, ordered nodes with slots the
	// model only has to FILL (queries, inputs, exact tool names) rather than
	// invent. This is the heart of the local-first pivot — a small local model
	// realises a given plan instead of designing an architecture from scratch.
	Plan []PlanStep `json:"plan,omitempty"`
}

// PlanStep is one node in a deterministic pre-plan skeleton. It pins the node's
// id, kind, role, output variable, and data dependencies; the model fills the
// concrete tool/agent name (from the catalog) and the input values.
type PlanStep struct {
	ID          string `json:"id"`              // verb-style node id
	Kind        string `json:"kind"`            // tool | agent | python | branch
	Description string `json:"description"`     // what this step does
	Uses        string `json:"uses"`            // capability hint ("web search tool", "notebooklm create-notebook tool", "summarizer agent")
	Output      string `json:"output"`          // output var downstream steps read
	Fills       string `json:"fills,omitempty"` // inputs the model must fill, incl. wiring (e.g. "notebook_id from create step")
}

// Patterns returns the curated pattern catalog. Pure + deterministic.
func Patterns() []Pattern {
	return []Pattern{
		{
			ID:          "notebooklm_podcast",
			Name:        "NotebookLM podcast / audio overview",
			Keywords:    []string{"notebooklm", "notebook lm", "audio overview", "podcast", "audio summary"},
			RequiresMCP: []string{"notebooklm"},
			Summary:     "Turn sources into a NotebookLM audio overview (podcast).",
			Steps: []string{
				"Create a NotebookLM notebook FIRST and capture its id as an output var (e.g. notebook_id). Nothing else can run before this.",
				"Add each source to that notebook, passing the notebook_id from step 1 (use {{ .notebook_id }}); do NOT hardcode or leave it blank.",
				"If the MCP exposes a readiness/status tool, poll or wait until the notebook reports ready before generating audio.",
				"Generate the audio overview, passing the SAME notebook_id from step 1. Generating audio with an empty or missing notebook_id is the #1 failure — always wire it from the create step.",
				"Deliver the resulting audio link/file to the requested channel, with a fallback message if generation failed.",
			},
			Plan: []PlanStep{
				{ID: "create_notebook", Kind: "tool", Description: "Create a new NotebookLM notebook", Uses: "the notebooklm create-notebook MCP tool", Output: "notebook_id", Fills: "the notebook title from the user's intent"},
				{ID: "add_sources", Kind: "tool", Description: "Add the source documents/URLs to the notebook", Uses: "the notebooklm add-source MCP tool", Output: "sources_added", Fills: "notebook_id = {{ .notebook_id }} (from create_notebook) + the sources from the intent"},
				{ID: "generate_audio", Kind: "tool", Description: "Generate the audio overview / podcast", Uses: "the notebooklm generate-audio MCP tool", Output: "audio", Fills: "notebook_id = {{ .notebook_id }} (from create_notebook)"},
				{ID: "deliver", Kind: "agent", Description: "Send the audio link to the user with a fallback if generation failed", Uses: "a delivery/notifier agent", Output: "sent", Fills: "the audio result {{ toJson .audio }} and the target channel"},
			},
		},
		{
			ID:       "search_and_rank",
			Name:     "Search, dedupe, and rank",
			Keywords: []string{"search", "research", "find articles", "news", "top stories", "latest", "gather", "scrape"},
			Summary:  "Fetch candidate results from the web/a source, clean them, and keep the best.",
			Steps: []string{
				"Run the search/fetch tool with a concrete query built from the intent (never an empty query).",
				"In a python node, parse the tool's result object defensively, DEDUPLICATE by url/title, and drop empty or low-quality items.",
				"Rank or trim to the requested count (e.g. top 5) and output a clean list var for downstream steps.",
				"Branch on whether any items survived: continue if yes, emit a graceful 'nothing found' path if no.",
			},
			Plan: []PlanStep{
				{ID: "search", Kind: "tool", Description: "Search the web for relevant results", Uses: "the web_search tool", Output: "results", Fills: "a concrete query built from the intent (never empty)"},
				{ID: "clean_rank", Kind: "python", Description: "Parse, dedupe by url/title, drop empties, keep the top N", Output: "items", Fills: "complete def run(inputs) reading inputs.get('results') defensively"},
				{ID: "have_items", Kind: "branch", Description: "Continue only if at least one item survived", Output: "", Fills: "edge.if predicate over {{ len .items }}"},
			},
		},
		{
			ID:       "scheduled_delivery",
			Name:     "Scheduled digest delivery",
			Keywords: []string{"every morning", "every day", "daily", "each morning", "weekly", "schedule", "7am", "8am", "send me", "deliver", "digest"},
			Summary:  "Produce content on a schedule and deliver it to a channel with status reporting.",
			Steps: []string{
				"Set a schedule trigger with a concrete cron derived from the phrasing (e.g. 'every morning at 7' -> 0 7 * * *).",
				"Run the content-producing steps (search/summarize/etc.).",
				"Format the final output for the target channel and send it to that EXACT configured channel.",
				"Handle the empty/error case explicitly: deliver a short fallback message rather than sending nothing or failing silently.",
			},
			Plan: []PlanStep{
				{ID: "produce", Kind: "agent", Description: "Produce the content for the digest", Uses: "a content/summarizer agent (or upstream tool steps)", Output: "content", Fills: "the task prompt + any upstream data"},
				{ID: "deliver", Kind: "agent", Description: "Format and send the result to the target channel, with a fallback when empty", Uses: "a delivery/notifier agent", Output: "sent", Fills: "the content {{ toJson .content }} and the EXACT configured channel"},
			},
		},
	}
}

// MatchPatterns returns the curated patterns relevant to the intent, ranked by
// match strength (keyword hits, with a small boost when a required MCP server is
// actually present in the catalog). Pure + deterministic. Returns at most `max`
// patterns (<=0 means no limit).
func MatchPatterns(intent string, cat Catalog, max int) []Pattern {
	li := strings.ToLower(intent)
	mcpPresent := map[string]bool{}
	for _, srv := range cat.MCP {
		mcpPresent[strings.ToLower(strings.TrimSpace(srv.Server))] = true
	}

	type scored struct {
		p     Pattern
		score int
	}
	var hits []scored
	for _, p := range Patterns() {
		score := 0
		for _, kw := range p.Keywords {
			if kw != "" && strings.Contains(li, strings.ToLower(kw)) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		for _, m := range p.RequiresMCP {
			if mcpPresent[strings.ToLower(m)] {
				score += 2 // the capability this pattern needs is actually installed
			}
		}
		hits = append(hits, scored{p: p, score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	var out []Pattern
	for _, h := range hits {
		out = append(out, h.p)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// writePatternGrounding appends the matched patterns to a compile prompt so the
// model follows the proven step order and data-flow contract. No-op when none
// match.
func writePatternGrounding(sb *strings.Builder, intent string, cat Catalog) {
	matched := MatchPatterns(intent, cat, 2)
	if len(matched) == 0 {
		return
	}
	sb.WriteString("\nPROVEN PATTERNS for this kind of request — follow this step order and data flow closely (adapt to the exact intent, but do not reorder dependent steps):\n")
	for _, p := range matched {
		sb.WriteString("• ")
		sb.WriteString(p.Name)
		sb.WriteString(" — ")
		sb.WriteString(p.Summary)
		sb.WriteString("\n")
		for i, step := range p.Steps {
			sb.WriteString("    ")
			sb.WriteString(itoa(i + 1))
			sb.WriteString(". ")
			sb.WriteString(step)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
