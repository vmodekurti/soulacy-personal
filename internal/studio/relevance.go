package studio

import (
	"sort"
	"strings"
)

// Grounding caps. Below these sizes the catalog is passed through UNCHANGED, so
// small/typical setups behave exactly as before and existing prompt tests hold.
// Above them, we keep the items most relevant to the intent (plus anything the
// intent names explicitly) to avoid flooding the prompt — which both wastes
// tokens and degrades weaker builder models.
const (
	maxGroundedSkills   = 24
	maxGroundedKBs      = 12
	maxGroundedMCPTools = 40
)

// FilterCatalogForIntent returns a copy of the catalog trimmed to the items most
// relevant to the intent, but ONLY when a list exceeds its cap. Agents, tools,
// providers, and channels are left untouched (small, cheap, and the user may
// reference any). Skills, KBs, and MCP tools — the token-heavy, often-large
// lists — are ranked by term overlap with the intent and capped; any item the
// intent mentions by name is always kept. Pure + deterministic.
func FilterCatalogForIntent(intent string, cat Catalog) Catalog {
	terms := tokenize(intent)
	li := strings.ToLower(intent)

	out := cat // shallow copy; we replace the slices we trim

	// Skills.
	if len(cat.Skills) > maxGroundedSkills {
		type sc struct {
			s     CatalogSkill
			score int
		}
		ranked := make([]sc, 0, len(cat.Skills))
		for _, s := range cat.Skills {
			score := topicalOverlap(terms, tokenize(s.Name+" "+s.Description))
			if nameMentioned(li, s.Name) {
				score += 100 // never drop an explicitly named skill
			}
			ranked = append(ranked, sc{s, score})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		trimmed := make([]CatalogSkill, 0, maxGroundedSkills)
		for i := 0; i < len(ranked) && i < maxGroundedSkills; i++ {
			// A cap is not a quota. Padding the shortlist out to 24 meant that on a
			// large install the model was handed the one relevant skill followed by
			// twenty-three with NO connection to the request at all — every one of
			// them an invitation to attach something irrelevant, which is exactly
			// what a travel agent carrying earnings-recap looks like.
			//
			// Zero score means nothing in the intent points at it. Sending fewer
			// skills is strictly better than sending noise: an empty list correctly
			// says "no installed skill fits this", which the model can act on.
			if ranked[i].score <= 0 {
				break
			}
			trimmed = append(trimmed, ranked[i].s)
		}
		out.Skills = trimmed
	}

	// Knowledge bases.
	if len(cat.KnowledgeBases) > maxGroundedKBs {
		type kc struct {
			k     CatalogKB
			score int
		}
		ranked := make([]kc, 0, len(cat.KnowledgeBases))
		for _, k := range cat.KnowledgeBases {
			score := topicalOverlap(terms, tokenize(k.Name+" "+k.Description))
			if nameMentioned(li, k.Name) {
				score += 100
			}
			ranked = append(ranked, kc{k, score})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		trimmed := make([]CatalogKB, 0, maxGroundedKBs)
		for i := 0; i < len(ranked) && i < maxGroundedKBs; i++ {
			trimmed = append(trimmed, ranked[i].k)
		}
		out.KnowledgeBases = trimmed
	}

	// MCP tools (across all servers). Keep whole servers, but cap total tools by
	// relevance. A server the intent names keeps all its tools.
	if total := countMCPTools(cat.MCP); total > maxGroundedMCPTools {
		out.MCP = trimMCPTools(cat.MCP, terms, li)
	}

	return out
}

// trimMCPTools keeps the most relevant MCP tools up to the cap, always keeping
// every tool of a server the intent names, and never dropping a server entirely
// if it still has at least one kept tool. Preserves server + tool order.
func trimMCPTools(servers []CatalogMCPServer, terms map[string]bool, li string) []CatalogMCPServer {
	type ref struct {
		srv, tool int
		score     int
	}
	var all []ref
	for si, srv := range servers {
		serverNamed := nameMentioned(li, srv.Server)
		for ti, t := range srv.Tools {
			score := topicalOverlap(terms, tokenize(t.Name+" "+t.Description))
			if serverNamed {
				score += 100
			}
			// Also protect a tool the intent names DIRECTLY.
			//
			// Matching only the server id misses the common case where the id is an
			// abbreviation: a server registered as "trvl" exposing "travel" is
			// invisible to a prompt that says "the travel tool", so the one tool the
			// user explicitly asked for could be trimmed out of the prompt while
			// thirty-nine tools they never mentioned survived.
			if bare := bareToolName(t.Name); bare != "" && nameMentioned(li, bare) {
				score += 100
			}
			all = append(all, ref{si, ti, score})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	keep := make(map[[2]int]bool)
	for i := 0; i < len(all) && i < maxGroundedMCPTools; i++ {
		keep[[2]int{all[i].srv, all[i].tool}] = true
	}

	out := make([]CatalogMCPServer, 0, len(servers))
	for si, srv := range servers {
		var tools []CatalogMCPTool
		for ti, t := range srv.Tools {
			if keep[[2]int{si, ti}] {
				tools = append(tools, t)
			}
		}
		if len(tools) > 0 {
			out = append(out, CatalogMCPServer{Server: srv.Server, Tools: tools})
		}
	}
	return out
}

// bareToolName strips the mcp__server__ prefix, leaving the word a user would
// actually write ("travel", not "mcp__trvl__travel"). Returns "" for names too
// short to match safely.
func bareToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(n, "__"); i >= 0 {
		n = n[i+2:]
	}
	if len(n) < 3 {
		return ""
	}
	return n
}

func countMCPTools(servers []CatalogMCPServer) int {
	n := 0
	for _, s := range servers {
		n += len(s.Tools)
	}
	return n
}

// tokenize lowercases, splits on non-alphanumerics, and drops very short words
// and a small stopword set, returning a set of meaningful terms.
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		w := cur.String()
		cur.Reset()
		if len(w) < 3 || stopwords[w] {
			return
		}
		// Normalise to the singular rather than indexing BOTH forms.
		//
		// Indexing both was a double-count: "updates" contributed "updates" AND
		// "update" to each side, so a single shared word scored 2 and cleared a
		// threshold meant to require two distinct topical matches. That is how a
		// shipping-lane skill stayed attached to a weather agent — one word in
		// common, counted twice.
		//
		// Folding both sides to one canonical form keeps the original benefit
		// ("flights" in a description still matches "flight" in the intent) with
		// no inflation. The stem does not need to be a real word — only the same
		// on both sides.
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			if sing := strings.TrimSuffix(w, "s"); len(sing) >= 3 && !stopwords[sing] {
				w = sing
			}
		}
		out[w] = true
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// overlap counts how many of b's terms appear in a.
func overlap(a, b map[string]bool) int {
	n := 0
	for t := range b {
		if a[t] {
			n++
		}
	}
	return n
}

// topicalOverlap is overlap ignoring words that carry no topical signal.
//
// This is what decides WHICH capabilities the builder model is even shown. With
// 210 skills installed and a cap of 24, ranking by raw shared-token count means
// the shortlist is whichever skills happen to share the most ordinary English
// with the prompt — "agent", "data", "search", "options", "tool". That is how a
// travel request produced a shortlist of options-payoff, earnings-recap and
// tradingview-reader: the model then chose from a candidate set that was
// already wrong, and no amount of care further down could recover from it.
//
// ground_skills.go had already learned this lesson and discounts generic tokens
// when it decides what to INJECT. The selection of what to show was still using
// the naive count.
func topicalOverlap(a, b map[string]bool) int {
	n := 0
	for t := range b {
		if a[t] && !genericTokens[t] {
			n++
		}
	}
	return n
}

// nameMentioned reports whether the (lowercased) intent contains the item name.
func nameMentioned(li, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name != "" && strings.Contains(li, name)
}

// genericTokens carry no topical signal no matter how small the catalog is.
//
// The IDF filter in groundSkills only discounts a token once it appears in more
// than a quarter of installed skills AND in more than five of them, so on a
// modest catalogue nothing is discounted and ordinary English — "agent",
// "user", "question", "options" — counts as evidence of a topic match. That is
// how a travel prompt pulled in funda-data and agent-creator: they share
// "data"/"agent" with almost any request. These are excluded unconditionally,
// which is what the document-frequency heuristic was reaching for and cannot
// express on a small sample.
var genericTokens = map[string]bool{
	"agent": true, "agents": true, "tool": true, "tools": true, "user": true,
	"users": true, "data": true, "info": true, "information": true,
	"question": true, "questions": true, "answer": true, "answers": true,
	"request": true, "requests": true, "response": true, "responses": true,
	"result": true, "results": true, "prompt": true, "prompts": true,
	"input": true, "inputs": true, "output": true, "outputs": true,
	"create": true, "creator": true, "build": true, "builder": true,
	"generate": true, "reader": true, "writer": true, "helper": true,
	"assistant": true, "service": true, "system": true, "context": true,
	"content": true, "text": true, "list": true, "find": true, "search": true,
	"send": true, "run": true, "call": true, "make": true, "set": true,
	"options": true, "option": true, "value": true, "values": true,
	"report": true, "reports": true, "analysis": true, "summary": true,
	"skill": true, "skills": true, "workflow": true, "conversational": true,
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "into": true, "your": true, "you": true, "are": true, "all": true,
	"use": true, "using": true, "get": true, "let": true, "via": true, "per": true,
	"can": true, "will": true, "every": true, "each": true, "when": true, "then": true,
	// Function words carry no topic anywhere, but the list above was short enough
	// that ordinary prepositions still scored: "answers questions ABOUT flight
	// options" matched "answer user questions ABOUT the results" on the word
	// "about", which was enough to put an earnings skill on a travel shortlist.
	"about": true, "over": true, "under": true, "some": true, "any": true,
	"more": true, "most": true, "than": true, "them": true, "they": true,
	"their": true, "there": true, "these": true, "those": true, "what": true,
	"which": true, "where": true, "who": true, "how": true, "why": true,
	"before": true, "after": true, "between": true, "during": true, "while": true,
	"also": true, "such": true, "only": true, "just": true, "been": true,
	"has": true, "have": true, "had": true, "was": true, "were": true, "its": true,
	"not": true, "but": true, "out": true, "off": true, "own": true, "same": true,
	"other": true, "another": true, "both": true, "few": true, "many": true,
	"one": true, "two": true, "new": true, "old": true, "now": true, "way": true,
	"like": true, "want": true, "need": true, "should": true, "would": true,
	"could": true, "must": true, "may": true, "might": true, "does": true,
	"did": true, "done": true, "goes": true, "given": true, "based": true,
}
