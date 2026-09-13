// retrieval.go — Story E29: pick the facts that matter for this turn and
// render them as a tiny system-prompt block.
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

// FormatPromptBlock renders facts as concise bullets under the memory
// heading, stopping before the token budget is exceeded. It returns "" when
// no fact fits so callers can skip the block entirely.
func FormatPromptBlock(facts []ScoredFact, tokenBudget int) string {
	if tokenBudget <= 0 {
		tokenBudget = DefaultPromptTokenBudget
	}
	var sb strings.Builder
	used := 0
	for _, f := range facts {
		line := "- " + f.Content
		cost := EstimateTokens(line)
		if used+cost > tokenBudget {
			if used == 0 {
				// Even a single fact is over budget: clip it rather than
				// drop all memory.
				line = clipToTokens(line, tokenBudget)
				sb.WriteString(line)
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
