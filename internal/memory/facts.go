// facts.go — adaptive memory: durable, user-scoped facts distilled from
// conversation turns.
//
// Adaptive memory is the fourth memory tier. Where the hot/archive/semantic
// tiers store *what was said*, adaptive memory stores *what is true about the
// user*: preferences, identity facts, constraints and named entities, plus the
// relationships between entities. Facts are extracted asynchronously after a
// turn, checked against existing facts so newer information supersedes or
// retracts stale information, and the most relevant handful is injected into
// the system prompt on the next turn.
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
	"regexp"
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

// BuiltinCategories are always accepted. Operators may add custom ones.
var BuiltinCategories = []FactCategory{FactPreference, FactIdentity, FactConstraint, FactEntity}

// Fact lifecycle states. Superseded and retracted facts are kept for audit
// and rollback; they never reach a prompt.
const (
	FactStatusActive     = "active"
	FactStatusSuperseded = "superseded" // replaced by a newer fact
	FactStatusRetracted  = "retracted"  // the user said it is no longer true
)

// ValidFactStatus reports whether s is a known lifecycle state.
func ValidFactStatus(s string) bool {
	return s == FactStatusActive || s == FactStatusSuperseded || s == FactStatusRetracted
}

var categorySlug = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// ValidFactCategory reports whether c is one of the four built-in categories.
func ValidFactCategory(c string) bool {
	return AllowedCategory(c, nil)
}

// AllowedCategory reports whether c is a built-in category or one of the
// operator-defined custom categories.
func AllowedCategory(c string, custom []string) bool {
	c = strings.ToLower(strings.TrimSpace(c))
	for _, b := range BuiltinCategories {
		if FactCategory(c) == b {
			return true
		}
	}
	for _, x := range custom {
		if c == strings.ToLower(strings.TrimSpace(x)) && categorySlug.MatchString(c) {
			return true
		}
	}
	return false
}

// NormalizeCategories lower-cases, validates and dedupes custom categories.
func NormalizeCategories(custom []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range custom {
		c = strings.ToLower(strings.TrimSpace(c))
		if !categorySlug.MatchString(c) || seen[c] || AllowedCategory(c, nil) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// FactScope is the tenancy boundary for adaptive memory. SessionID is not
// part of the boundary; it lets recall include facts that were marked
// session-scoped ("temporary") when they were extracted in the same session.
type FactScope struct {
	Workspace string `json:"workspace"`
	Owner     string `json:"owner"`
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
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
	s.SessionID = strings.TrimSpace(s.SessionID)
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
	Source          string `json:"source"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	SourceRunID     string `json:"source_run_id,omitempty"`
	// SessionScoped facts are only recalled inside the session that produced
	// them ("for this conversation, answer in French").
	SessionScoped bool `json:"session_scoped,omitempty"`
	// ExpiresAt, when set, hides the fact from recall after that instant.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Embedding is populated by the local engine; it is never serialised to
	// API clients.
	Embedding []float32 `json:"-"`
}

// Expired reports whether the fact has passed its expiry.
func (f Fact) Expired(now time.Time) bool {
	return f.ExpiresAt != nil && !f.ExpiresAt.IsZero() && !now.Before(*f.ExpiresAt)
}

// Relation is a graph edge between two entities, extracted from the same
// turn as facts ("User" → "has dog" → "Rex"). Relations are scoped exactly
// like facts.
type Relation struct {
	ID              string    `json:"id"`
	Workspace       string    `json:"workspace"`
	Owner           string    `json:"owner"`
	AgentID         string    `json:"agent_id"`
	Subject         string    `json:"subject"`
	Predicate       string    `json:"predicate"`
	Object          string    `json:"object"`
	Status          string    `json:"status"`
	Source          string    `json:"source"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	SourceRunID     string    `json:"source_run_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Sentence renders the relation as a short prompt line.
func (r Relation) Sentence() string {
	return r.Subject + " " + r.Predicate + " " + r.Object
}

// FactEvent is one entry in a fact's change history.
type FactEvent struct {
	ID        int64        `json:"id"`
	FactID    string       `json:"fact_id"`
	Event     string       `json:"event"` // created | updated | superseded | retracted | deleted | expired
	Content   string       `json:"content"`
	Category  FactCategory `json:"category"`
	Status    string       `json:"status"`
	Actor     string       `json:"actor"` // "extractor", "user", "provider"
	CreatedAt time.Time    `json:"created_at"`
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
	Candidates     int      `json:"candidates"`
	Added          []string `json:"added,omitempty"`
	Superseded     []string `json:"superseded,omitempty"`
	Retracted      []string `json:"retracted,omitempty"`
	RelationsAdded int      `json:"relations_added"`
	Skipped        int      `json:"skipped"`
	Provider       string   `json:"provider"`
	ExtractedAt    time.Time
}

// FactInput carries the optional attributes of a manual add or edit.
type FactInput struct {
	Category  FactCategory
	Content   string
	ExpiresAt *time.Time
}

// Adaptive is the provider-agnostic contract the runtime and gateway use.
// The local engine and the Mem0 adapter both satisfy it.
type Adaptive interface {
	// Provider names the backing engine: "local" or "mem0".
	Provider() string
	// Categories lists every category the engine accepts (built-in + custom).
	Categories() []string
	// Remember distills a completed turn into facts and relations and
	// reconciles them with what is already known. It must be safe to call
	// from a background goroutine and must never mutate storage for turns
	// that carry no facts.
	Remember(ctx context.Context, scope FactScope, turn Turn) (RememberOutcome, error)
	// Recall returns the active facts most relevant to query, best first.
	Recall(ctx context.Context, scope FactScope, query string, limit int) ([]ScoredFact, error)
	// Relations returns active entity relations relevant to query (or the
	// most recent when query is empty).
	Relations(ctx context.Context, scope FactScope, query string, limit int) ([]Relation, error)
	// List returns facts in the scope filtered by status ("" = all).
	List(ctx context.Context, scope FactScope, status string, limit int) ([]Fact, error)
	// Add stores a fact the user wrote by hand.
	Add(ctx context.Context, scope FactScope, in FactInput) (Fact, error)
	// Update rewrites a fact's content, category and/or expiry.
	Update(ctx context.Context, scope FactScope, id string, in FactInput) (Fact, error)
	// Delete removes one fact permanently.
	Delete(ctx context.Context, scope FactScope, id string) error
	// History returns the change log of one fact, newest first.
	History(ctx context.Context, scope FactScope, id string) ([]FactEvent, error)
	// Purge removes every fact and relation in the scope and returns how
	// many facts were removed.
	Purge(ctx context.Context, scope FactScope) (int64, error)
	Close() error
}

// Sentinel errors shared by all providers.
var (
	ErrFactNotFound   = errors.New("adaptive memory: fact not found")
	ErrInvalidFact    = errors.New("adaptive memory: invalid fact")
	ErrInvalidScope   = errors.New("adaptive memory: owner is required")
	ErrProviderFailed = errors.New("adaptive memory: provider request failed")
	ErrUnsupported    = errors.New("adaptive memory: not supported by this provider")
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

// FactChange is one entry of the change feed a device syncs from. Deleted
// facts (and facts removed by a purge) arrive as tombstones with no Fact.
type FactChange struct {
	Cursor  int64  `json:"cursor"`
	FactID  string `json:"fact_id"`
	Event   string `json:"event"`
	Deleted bool   `json:"deleted"`
	Fact    *Fact  `json:"fact,omitempty"`
}

// FactChanges is a page of the feed. Reset tells the device its cursor is
// ahead of anything the gateway knows (a purge happened): drop the cache
// and start from zero.
type FactChanges struct {
	Changes    []FactChange `json:"changes"`
	NextCursor int64        `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
	Reset      bool         `json:"reset"`
}

// ChangeFeed is implemented by engines that can serve an incremental sync;
// the built-in engine does, an external provider may not.
type ChangeFeed interface {
	Changes(ctx context.Context, scope FactScope, since int64, limit int) (FactChanges, error)
}
