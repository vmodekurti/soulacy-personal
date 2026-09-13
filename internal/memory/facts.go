// facts.go — adaptive memory: durable, user-scoped facts distilled from
// conversation turns.
//
// Adaptive memory is the fourth memory tier. Where the hot/archive/semantic
// tiers store *what was said*, adaptive memory stores *what is true about the
// user*: preferences, identity facts, constraints and named entities. Facts
// are extracted asynchronously after a turn, checked against existing facts so
// newer information supersedes stale information, and the most relevant
// handful is injected into the system prompt on the next turn.
//
// Every fact is scoped by (workspace, owner, agent). The owner is the
// authenticated principal that produced the turn; the runtime never reads
// facts across owners, and the gateway enforces the same boundary for the
// management API.
package memory

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// FactCategory classifies an extracted fact.
type FactCategory string

const (
	FactPreference FactCategory = "preference"
	FactIdentity   FactCategory = "identity"
	FactConstraint FactCategory = "constraint"
	FactEntity     FactCategory = "entity"
)

// FactStatusActive / FactStatusSuperseded are the two lifecycle states. A
// superseded fact is kept for audit and rollback; it never reaches a prompt.
const (
	FactStatusActive     = "active"
	FactStatusSuperseded = "superseded"
)

// ValidFactCategory reports whether c is one of the four supported categories.
func ValidFactCategory(c string) bool {
	switch FactCategory(strings.ToLower(strings.TrimSpace(c))) {
	case FactPreference, FactIdentity, FactConstraint, FactEntity:
		return true
	}
	return false
}

// FactScope is the tenancy boundary for adaptive memory.
type FactScope struct {
	Workspace string `json:"workspace"`
	Owner     string `json:"owner"`
	AgentID   string `json:"agent_id"`
}

// Normalize fills the workspace default and trims fields. An empty owner is
// invalid: facts are always attributed to an authenticated identity.
func (s FactScope) Normalize() FactScope {
	s.Workspace = strings.TrimSpace(s.Workspace)
	if s.Workspace == "" {
		s.Workspace = DefaultWorkspace
	}
	s.Owner = strings.TrimSpace(s.Owner)
	s.AgentID = strings.TrimSpace(s.AgentID)
	return s
}

// DefaultWorkspace is used when the caller has no workspace concept (Personal).
const DefaultWorkspace = "default"

// Fact is one durable memory record.
type Fact struct {
	ID           string       `json:"id"`
	Workspace    string       `json:"workspace"`
	Owner        string       `json:"owner"`
	AgentID      string       `json:"agent_id"`
	Category     FactCategory `json:"category"`
	Content      string       `json:"content"`
	Confidence   float32      `json:"confidence"`
	Status       string       `json:"status"`
	SupersededBy string       `json:"superseded_by,omitempty"`
	Supersedes   string       `json:"supersedes,omitempty"`
	// Source records where the fact came from: "extracted" (from a turn),
	// "manual" (added in the GUI) or "provider" (an external engine).
	Source          string    `json:"source"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	SourceRunID     string    `json:"source_run_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Embedding is populated by the local engine; it is never serialised to
	// API clients.
	Embedding []float32 `json:"-"`
}

// ScoredFact is a fact plus its retrieval score in [0,1].
type ScoredFact struct {
	Fact
	Score float64 `json:"score"`
}

// Turn is the material handed to Remember after a completed exchange.
type Turn struct {
	SessionID string
	RunID     string
	User      string
	Assistant string
}

// RememberOutcome summarises what one Remember call did. It exists so tests
// and the activity log can see the pipeline's decisions without reading rows.
type RememberOutcome struct {
	Candidates  int      `json:"candidates"`
	Added       []string `json:"added,omitempty"`
	Superseded  []string `json:"superseded,omitempty"`
	Skipped     int      `json:"skipped"`
	Provider    string   `json:"provider"`
	ExtractedAt time.Time
}

// Adaptive is the provider-agnostic contract the runtime and gateway use.
// The local engine and the Mem0 adapter both satisfy it.
type Adaptive interface {
	// Provider names the backing engine: "local" or "mem0".
	Provider() string
	// Remember distills a completed turn into facts and reconciles them with
	// what is already known. It must be safe to call from a background
	// goroutine and must never mutate storage for turns that carry no facts.
	Remember(ctx context.Context, scope FactScope, turn Turn) (RememberOutcome, error)
	// Recall returns the active facts most relevant to query, best first.
	Recall(ctx context.Context, scope FactScope, query string, limit int) ([]ScoredFact, error)
	// List returns facts in the scope filtered by status ("" = all).
	List(ctx context.Context, scope FactScope, status string, limit int) ([]Fact, error)
	// Add stores a fact the user wrote by hand.
	Add(ctx context.Context, scope FactScope, category FactCategory, content string) (Fact, error)
	// Update rewrites a fact's content and/or category.
	Update(ctx context.Context, scope FactScope, id, content string, category FactCategory) (Fact, error)
	// Delete removes one fact permanently.
	Delete(ctx context.Context, scope FactScope, id string) error
	// Purge removes every fact in the scope and returns how many were removed.
	Purge(ctx context.Context, scope FactScope) (int64, error)
	Close() error
}

// Sentinel errors shared by all providers.
var (
	ErrFactNotFound   = errors.New("adaptive memory: fact not found")
	ErrInvalidFact    = errors.New("adaptive memory: invalid fact")
	ErrInvalidScope   = errors.New("adaptive memory: owner is required")
	ErrProviderFailed = errors.New("adaptive memory: provider request failed")
)

// MaxFactContent bounds a single fact. Facts are meant to be one-line
// statements; anything longer is a transcript, not a fact.
const MaxFactContent = 400

// NormalizeFactContent trims and collapses whitespace and rejects empty or
// oversized content.
func NormalizeFactContent(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", ErrInvalidFact
	}
	if len(s) > MaxFactContent {
		return "", ErrInvalidFact
	}
	return s, nil
}

// ── scoring helpers shared by the local engine ──────────────────────────────

// CosineSimilarity returns the cosine of the angle between a and b, or 0 when
// either vector is empty or zero-length.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Tokenize lower-cases s and splits it into alphanumeric keyword tokens,
// dropping very short tokens and a small stop list. It backs the keyword half
// of hybrid retrieval, so it must not depend on an FTS build of SQLite.
func Tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 {
			w := cur.String()
			if !stopWords[w] {
				out = append(out, w)
			}
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"you": true, "your": true, "are": true, "was": true, "have": true, "has": true,
	"not": true, "but": true, "can": true, "please": true, "what": true, "how": true,
	"about": true, "from": true, "into": true, "when": true, "will": true, "would": true,
	"like": true, "just": true, "than": true, "then": true, "them": true, "they": true,
	"user": true, "want": true, "need": true, "does": true, "did": true, "also": true,
}

// KeywordOverlap returns |query ∩ doc| / |query| over token sets, in [0,1].
func KeywordOverlap(queryTokens []string, doc string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, t := range Tokenize(doc) {
		set[t] = true
	}
	hits := 0
	seen := map[string]bool{}
	for _, q := range queryTokens {
		if seen[q] {
			continue
		}
		seen[q] = true
		if set[q] {
			hits++
		}
	}
	return float64(hits) / float64(len(seen))
}

// EstimateTokens approximates LLM token usage for budgeting prompt blocks.
// It deliberately over-counts (¾ word per token, plus punctuation) so a
// "<50 tokens" cap holds for real tokenizers.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := len(strings.Fields(s))
	punct := 0
	for _, r := range s {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			punct++
		}
	}
	return int(math.Ceil(float64(words)*1.3)) + punct/2
}

// SortScored orders best-first with a stable tiebreak on recency then ID.
func SortScored(items []ScoredFact) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
}
