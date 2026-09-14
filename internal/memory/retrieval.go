// retrieval.go — Story E29: pick the facts and relations that matter for
// this turn and render them as a tiny system-prompt block.
//
// Retrieval is hybrid: embedding similarity finds facts that are about the
// same thing as the query even when the words differ, and keyword overlap
// catches exact names and terms that embeddings blur. Both halves run over
// facts already filtered to (workspace, owner, agent), so tenancy is enforced
// by the store query, not by ranking.
package memory

import (
	"strings"
)

// Retrieval weights. Keyword overlap is a strong signal when present but is
// usually zero, so the vector half carries most of the ordering.
const (
	weightVector  = 0.7
	weightKeyword = 0.3
	// recencyBonus nudges recently updated facts ahead on ties.
	recencyBonus = 0.02
)

// RankFacts scores active facts against a query embedding and tokens and
// returns the top limit results, best first. Facts with no signal at all are
// still eligible when the query is empty (a "profile" recall), ordered by
// recency.
func RankFacts(facts []Fact, queryVec []float32, queryTokens []string, limit int) []ScoredFact {
	if limit <= 0 {
		limit = 5
	}
	out := make([]ScoredFact, 0, len(facts))
	for i, f := range facts {
		if f.Status != FactStatusActive {
			continue
		}
		var score float64
		if len(queryVec) > 0 && len(f.Embedding) == len(queryVec) {
			score += weightVector * clamp01(CosineSimilarity(queryVec, f.Embedding))
		}
		if len(queryTokens) > 0 {
			score += weightKeyword * KeywordOverlap(queryTokens, f.Content)
		}
		// Recency: facts are supplied newest-first by the store, so earlier
		// index gets a slightly larger bonus.
		if len(facts) > 1 {
			score += recencyBonus * (1 - float64(i)/float64(len(facts)-1))
		}
		out = append(out, ScoredFact{Fact: f, Score: score})
	}
	SortScored(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RankRelations orders active relations by keyword overlap with the query
// (subject, predicate and object all count), then recency, and returns the
// top limit. With an empty query the newest relations win.
func RankRelations(rels []Relation, queryTokens []string, limit int) []Relation {
	if limit <= 0 {
		limit = 3
	}
	type scored struct {
		r     Relation
		score float64
	}
	items := make([]scored, 0, len(rels))
	for i, r := range rels {
		if r.Status != FactStatusActive {
			continue
		}
		s := KeywordOverlap(queryTokens, r.Sentence())
		if len(rels) > 1 {
			s += recencyBonus * (1 - float64(i)/float64(len(rels)-1))
		}
		items = append(items, scored{r, s})
	}
	// insertion sort keeps this dependency-free and the lists are tiny
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].score > items[j-1].score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	out := make([]Relation, 0, limit)
	for _, it := range items {
		if len(queryTokens) > 0 && it.score < weightKeyword*0.34 && len(out) > 0 {
			// With a query, only surface relations that actually match
			// beyond the first fallback item.
			continue
		}
		out = append(out, it.r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// PromptBlockHeader is the exact heading agents see. Keep it stable: users
// and tests look for it.
const PromptBlockHeader = "### 🧠 MEMORY & PREFERENCES"

// DefaultPromptTokenBudget caps the injected block so memory never crowds out
// the task. Fifty tokens is roughly five short bullets.
const DefaultPromptTokenBudget = 50

// FormatPromptBlock renders facts (and then relations, if budget remains) as
// concise bullets under the memory heading, stopping before the token budget
// is exceeded. It returns "" when nothing fits so callers can skip the block.
func FormatPromptBlock(facts []ScoredFact, tokenBudget int, relations ...Relation) string {
	if tokenBudget <= 0 {
		tokenBudget = DefaultPromptTokenBudget
	}
	var sb strings.Builder
	used := 0
	lines := make([]string, 0, len(facts)+len(relations))
	for _, f := range facts {
		lines = append(lines, "- "+f.Content)
	}
	for _, r := range relations {
		lines = append(lines, "- "+r.Sentence())
	}
	for i, line := range lines {
		cost := EstimateTokens(line)
		if used+cost > tokenBudget {
			if i == 0 {
				// Even a single fact is over budget: clip it rather than
				// drop all memory.
				sb.WriteString(clipToTokens(line, tokenBudget))
				sb.WriteString("\n")
			}
			break
		}
		used += cost
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return ""
	}
	return PromptBlockHeader + "\n" + sb.String()
}

func clipToTokens(line string, budget int) string {
	words := strings.Fields(line)
	for len(words) > 2 && EstimateTokens(strings.Join(words, " ")) > budget {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ") + "…"
}
