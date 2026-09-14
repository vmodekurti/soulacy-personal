// extractor.go — Story E27: distill a completed turn into candidate facts
// and entity relations.
//
// The extractor is deliberately cheap. Its default prompt is under 200 input
// tokens including the turn itself, it asks for JSON only, and it is happy to
// return nothing: casual turns ("thanks", "hello") must produce no candidates
// and therefore no database writes. Operators may append short custom
// instructions and custom categories; those add to the budget and are capped.
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
	// Temporary marks a fact that only applies to the current conversation
	// ("for this chat, answer in French"). Stored session-scoped.
	Temporary bool `json:"temporary,omitempty"`
}

// RelationCandidate is one extracted entity relation.
type RelationCandidate struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// Extraction is the parsed model output.
type Extraction struct {
	Facts     []Candidate         `json:"facts"`
	Relations []RelationCandidate `json:"relations"`
}

// ExtractOptions customise the extraction prompt.
type ExtractOptions struct {
	// Instructions is operator guidance appended to the system prompt, for
	// example "Also capture the user's job title and team." Capped at
	// MaxInstructions bytes.
	Instructions string
	// CustomCategories extends the four built-in categories.
	CustomCategories []string
}

// MaxInstructions bounds operator instructions so the prompt stays small.
const MaxInstructions = 240

// Extraction budgets. The default system prompt is ~65 tokens; the clipped
// turn adds at most ~105 (measured with the conservative EstimateTokens),
// keeping every default extraction call under the 200-token cap.
const (
	extractUserClip      = 300
	extractAssistantClip = 100
	// minExtractableTurn skips the model entirely for turns that cannot
	// contain a durable fact.
	minExtractableTurn = 12
)

const extractSystemPrompt = `Extract durable facts about the user and entity relations. Ignore greetings, questions, the assistant. JSON only: {"facts":[{"fact":"short third-person sentence","category":"%s","confidence":0-1,"temporary":false}],"relations":[{"subject":"User","predicate":"has dog","object":"Rex"}]}. temporary=true only for this-conversation-only requests. Empty lists if nothing durable.`

// ExtractionPrompt returns the system and user strings sent to the model for
// a turn. It is exported so tests can assert the token budget.
func ExtractionPrompt(turn Turn, opts ExtractOptions) (system, user string) {
	cats := make([]string, 0, 4+len(opts.CustomCategories))
	for _, c := range BuiltinCategories {
		cats = append(cats, string(c))
	}
	cats = append(cats, NormalizeCategories(opts.CustomCategories)...)
	system = strings.Replace(extractSystemPrompt, "%s", strings.Join(cats, "|"), 1)
	if ins := strings.Join(strings.Fields(opts.Instructions), " "); ins != "" {
		if len(ins) > MaxInstructions {
			ins = ins[:MaxInstructions]
		}
		system += " " + ins
	}
	u := clip(strings.TrimSpace(turn.User), extractUserClip)
	a := clip(strings.TrimSpace(turn.Assistant), extractAssistantClip)
	var sb strings.Builder
	sb.WriteString("USER: ")
	sb.WriteString(u)
	if a != "" {
		sb.WriteString("\nASSISTANT: ")
		sb.WriteString(a)
	}
	return system, sb.String()
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

// Extract runs the extraction prompt and parses the result. A malformed
// model reply is treated as "nothing" rather than an error, so a flaky small
// model can never poison memory.
func Extract(ctx context.Context, complete Completer, turn Turn, opts ExtractOptions) (Extraction, error) {
	if complete == nil {
		return Extraction{}, errors.New("adaptive memory: no completer configured")
	}
	if !ShouldExtract(turn) {
		return Extraction{}, nil
	}
	system, user := ExtractionPrompt(turn, opts)
	raw, err := complete(ctx, system, user, 400)
	if err != nil {
		return Extraction{}, err
	}
	return ParseExtraction(raw, opts.CustomCategories), nil
}

// ParseCandidates decodes fact candidates only (built-in categories). Kept
// for callers and tests that predate relations.
func ParseCandidates(raw string) []Candidate {
	return ParseExtraction(raw, nil).Facts
}

// ParseExtraction decodes the model output, tolerating code fences and prose
// around the JSON, a bare array of facts, or the {"facts","relations"}
// object, and drops anything that is not usable.
func ParseExtraction(raw string, custom []string) Extraction {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var ext Extraction
	objStart, objEnd := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	arrStart, arrEnd := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	switch {
	case objStart >= 0 && objEnd > objStart && (arrStart < 0 || objStart < arrStart):
		if json.Unmarshal([]byte(raw[objStart:objEnd+1]), &ext) != nil {
			return Extraction{}
		}
	case arrStart >= 0 && arrEnd > arrStart:
		if json.Unmarshal([]byte(raw[arrStart:arrEnd+1]), &ext.Facts) != nil {
			return Extraction{}
		}
	default:
		return Extraction{}
	}
	return Extraction{Facts: cleanCandidates(ext.Facts, custom), Relations: cleanRelations(ext.Relations)}
}

func cleanCandidates(items []Candidate, custom []string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, c := range items {
		content, err := NormalizeFactContent(c.Fact)
		if err != nil {
			continue
		}
		c.Fact = content
		c.Category = strings.ToLower(strings.TrimSpace(c.Category))
		if !AllowedCategory(c.Category, custom) {
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
	return out
}

func cleanRelations(items []RelationCandidate) []RelationCandidate {
	var out []RelationCandidate
	seen := map[string]bool{}
	for _, r := range items {
		r.Subject = strings.Join(strings.Fields(r.Subject), " ")
		r.Predicate = strings.ToLower(strings.Join(strings.Fields(r.Predicate), " "))
		r.Object = strings.Join(strings.Fields(r.Object), " ")
		if r.Subject == "" || r.Predicate == "" || r.Object == "" || len(r.Subject) > 120 || len(r.Predicate) > 60 || len(r.Object) > 120 {
			continue
		}
		key := strings.ToLower(r.Subject + "|" + r.Predicate + "|" + r.Object)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
		if len(out) >= 8 {
			break
		}
	}
	return out
}
