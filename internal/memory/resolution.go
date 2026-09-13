// resolution.go — Story E28: reconcile a candidate fact with what is already
// known so memory evolves instead of accumulating contradictions.
//
// For each candidate the engine finds the most similar active facts by
// embedding cosine similarity. Above the similarity threshold a decision is
// needed: does the candidate SUPERSEDE the old fact (same subject, new value),
// COMPLEMENT it (related but compatible detail), or is it a NO-OP duplicate?
// A cheap arbitration prompt decides; if the model is unavailable, a
// conservative rule decides instead so the pipeline never stalls.
package memory

import (
	"context"
	"encoding/json"
	"strings"
)

// Decision is the outcome of arbitration for one candidate.
type Decision string

const (
	DecisionSupersede  Decision = "supersede"
	DecisionComplement Decision = "complement"
	DecisionNoop       Decision = "noop"
)

// Resolution pairs a decision with the fact it applies to (nil for complement
// when no conflicting fact exists).
type Resolution struct {
	Decision Decision
	Existing *Fact
	Reason   string
}

// DefaultSimilarityThreshold is the cosine similarity above which two facts
// are considered to be about the same thing.
const DefaultSimilarityThreshold = 0.85

const arbitrationSystemPrompt = `Two short facts about the same user. Decide how the NEW fact relates to the OLD one. Reply JSON only: {"decision":"supersede|complement|noop"}. supersede = same subject, the new value replaces the old (moved, changed preference, corrected). complement = compatible extra detail. noop = same information restated.`

// ResolveCandidate compares candidate against active facts and returns what
// should happen. similar must already be filtered to the caller's scope.
func ResolveCandidate(ctx context.Context, complete Completer, candidate Fact, similar []ScoredFact, threshold float64) Resolution {
	if threshold <= 0 {
		threshold = DefaultSimilarityThreshold
	}
	var best *ScoredFact
	for i := range similar {
		if similar[i].Score >= threshold && (best == nil || similar[i].Score > best.Score) {
			best = &similar[i]
		}
	}
	if best == nil {
		return Resolution{Decision: DecisionComplement, Reason: "no similar active fact"}
	}
	if strings.EqualFold(best.Content, candidate.Content) {
		return Resolution{Decision: DecisionNoop, Existing: &best.Fact, Reason: "identical content"}
	}
	if complete != nil {
		user := "OLD: " + best.Content + "\nNEW: " + candidate.Content
		if raw, err := complete(ctx, arbitrationSystemPrompt, user, 40); err == nil {
			if d, ok := parseDecision(raw); ok {
				return Resolution{Decision: d, Existing: &best.Fact, Reason: "model arbitration"}
			}
		}
	}
	return Resolution{Decision: ruleDecision(best.Score, candidate, best.Fact), Existing: &best.Fact, Reason: "rule arbitration"}
}

func parseDecision(raw string) (Decision, bool) {
	raw = strings.TrimSpace(raw)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		var out struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal([]byte(raw[i:j+1]), &out) == nil {
			raw = out.Decision
		}
	}
	switch Decision(strings.ToLower(strings.Trim(raw, `" .`))) {
	case DecisionSupersede:
		return DecisionSupersede, true
	case DecisionComplement:
		return DecisionComplement, true
	case DecisionNoop:
		return DecisionNoop, true
	}
	return "", false
}

// ruleDecision is the model-free fallback. Very high similarity with the same
// category is a restatement; high similarity with the same category but
// different wording is treated as an update; a different category is extra
// detail.
func ruleDecision(score float64, candidate, existing Fact) Decision {
	if candidate.Category != existing.Category {
		return DecisionComplement
	}
	if score >= 0.97 {
		return DecisionNoop
	}
	return DecisionSupersede
}
