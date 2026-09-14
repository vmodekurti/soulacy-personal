// resolution.go — Story E28: reconcile a candidate fact with what is already
// known so memory evolves instead of accumulating contradictions.
//
// For each candidate the engine finds the most similar active facts by
// embedding cosine similarity. Above the similarity threshold a decision is
// needed: does the candidate SUPERSEDE the old fact (same subject, new value),
// RETRACT it (the user says it is no longer true), COMPLEMENT it (related but
// compatible detail), or is it a NO-OP duplicate? A cheap arbitration prompt
// decides; if the model is unavailable, a conservative rule decides instead
// so the pipeline never stalls.
package memory

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Decision is the outcome of arbitration for one candidate.
type Decision string

const (
	DecisionSupersede  Decision = "supersede"
	DecisionRetract    Decision = "retract"
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

const arbitrationSystemPrompt = `Two short facts about the same user. Decide how the NEW fact relates to the OLD one. Reply JSON only: {"decision":"supersede|retract|complement|noop"}. supersede = same subject, the new value replaces the old (moved, changed preference, corrected). retract = the new statement says the old fact is no longer true or never was, without giving a replacement. complement = compatible extra detail. noop = same information restated.`

// negation spots candidates that state something stopped being true, so the
// rule fallback can retract without a model.
var negation = regexp.MustCompile(`(?i)\b(no longer|not anymore|anymore|stopped|quit|never|doesn't|does not|don't|do not|isn't|is not|used to)\b`)

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
	case DecisionRetract:
		return DecisionRetract, true
	case DecisionComplement:
		return DecisionComplement, true
	case DecisionNoop:
		return DecisionNoop, true
	}
	return "", false
}

// ruleDecision is the model-free fallback. A negating candidate retracts;
// very high similarity with the same category is a restatement; high
// similarity with the same category but different wording is an update; a
// different category is extra detail.
func ruleDecision(score float64, candidate, existing Fact) Decision {
	if negation.MatchString(candidate.Content) && !negation.MatchString(existing.Content) {
		return DecisionRetract
	}
	if candidate.Category != existing.Category {
		return DecisionComplement
	}
	if score >= 0.97 {
		return DecisionNoop
	}
	return DecisionSupersede
}
