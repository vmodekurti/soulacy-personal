// extractor.go — Story E27: distill a completed turn into candidate facts.
//
// The extractor is deliberately cheap. Its prompt is under 200 input tokens
// including the turn itself, it asks for JSON only, and it is happy to return
// an empty array: casual turns ("thanks", "hello") must produce no candidates
// and therefore no database writes.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Completer is the minimal LLM contract the memory package needs. The runtime
// supplies one that routes to the configured lightweight model and attaches
// governance metadata; tests supply a canned function.
type Completer func(ctx context.Context, system, user string, maxTokens int) (string, error)

// Candidate is one extracted fact before reconciliation.
type Candidate struct {
	Fact       string  `json:"fact"`
	Category   string  `json:"category"`
	Confidence float32 `json:"confidence"`
}

// Extraction budgets. The system prompt is ~75 tokens; the clipped turn adds
// at most ~110, keeping every extraction call under the 200-token cap.
const (
	extractUserClip      = 300
	extractAssistantClip = 100
	// minExtractableTurn skips the model entirely for turns that cannot
	// contain a durable fact.
	minExtractableTurn = 12
)

const extractSystemPrompt = `Extract durable facts about the user: preferences, identity, constraints, related entities. Ignore greetings, questions, and the assistant. JSON only: [{"fact":"one short third-person sentence","category":"preference|identity|constraint|entity","confidence":0-1}]. Return [] if nothing durable.`

// ExtractionPrompt returns the system and user strings sent to the model for
// a turn. It is exported so tests can assert the token budget.
func ExtractionPrompt(turn Turn) (system, user string) {
	u := clip(strings.TrimSpace(turn.User), extractUserClip)
	a := clip(strings.TrimSpace(turn.Assistant), extractAssistantClip)
	var sb strings.Builder
	sb.WriteString("USER: ")
	sb.WriteString(u)
	if a != "" {
		sb.WriteString("\nASSISTANT: ")
		sb.WriteString(a)
	}
	return extractSystemPrompt, sb.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexAny(cut, " \n"); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// casualTurn is a fast, model-free filter for turns that carry no facts.
var casualTurn = regexp.MustCompile(`(?i)^\s*(hi|hello|hey|thanks?|thank you|thx|ok(ay)?|cool|great|nice|yes|no|yep|nope|bye|good (morning|night|evening)|lol|👍|🙏)[\s!.,]*$`)

// ShouldExtract reports whether a turn is worth an extraction call.
func ShouldExtract(turn Turn) bool {
	u := strings.TrimSpace(turn.User)
	if len(u) < minExtractableTurn {
		return false
	}
	return !casualTurn.MatchString(u)
}

// Extract runs the extraction prompt and parses candidates. A malformed model
// reply is treated as "no facts" rather than an error, so a flaky small model
// can never poison memory.
func Extract(ctx context.Context, complete Completer, turn Turn) ([]Candidate, error) {
	if complete == nil {
		return nil, errors.New("adaptive memory: no completer configured")
	}
	if !ShouldExtract(turn) {
		return nil, nil
	}
	system, user := ExtractionPrompt(turn)
	raw, err := complete(ctx, system, user, 320)
	if err != nil {
		return nil, err
	}
	return ParseCandidates(raw), nil
}

// ParseCandidates decodes the model output, tolerating code fences and prose
// around the JSON array, and drops anything that is not a usable fact.
func ParseCandidates(raw string) []Candidate {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	start, end := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	var items []Candidate
	if err := json.Unmarshal([]byte(raw[start:end+1]), &items); err != nil {
		return nil
	}
	out := items[:0]
	seen := map[string]bool{}
	for _, c := range items {
		content, err := NormalizeFactContent(c.Fact)
		if err != nil {
			continue
		}
		c.Fact = content
		c.Category = strings.ToLower(strings.TrimSpace(c.Category))
		if !ValidFactCategory(c.Category) {
			continue
		}
		if c.Confidence <= 0 || c.Confidence > 1 {
			c.Confidence = 0.5
		}
		key := strings.ToLower(content)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
