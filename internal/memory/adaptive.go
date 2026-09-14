// adaptive.go — the local adaptive memory engine.
//
// LocalAdaptive ties the pieces together: extraction (E27) produces fact and
// relation candidates, resolution (E28) reconciles each fact against the
// owner's active facts (supersede / retract / complement / noop), relations
// are upserted with same-subject-and-predicate supersession, and retrieval
// (E29) serves the prompt block. Storage is the SQLite fact store; embeddings
// come from whichever Embedder the host wires in (the same one Knowledge
// uses). Everything is scoped by FactScope.
package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// LocalOptions tunes the local engine.
type LocalOptions struct {
	// MinConfidence drops low-confidence candidates before reconciliation.
	MinConfidence float32
	// SimilarityThreshold is the cosine similarity above which a candidate is
	// compared against an existing fact (default 0.85).
	SimilarityThreshold float64
	// MaxRecall caps how many facts Recall returns (default 5).
	MaxRecall int
	// Instructions is operator guidance appended to the extraction prompt.
	Instructions string
	// CustomCategories extends the built-in fact categories.
	CustomCategories []string
	// GraphEnabled turns relation extraction on (default true).
	GraphDisabled bool
}

func (o LocalOptions) withDefaults() LocalOptions {
	if o.MinConfidence <= 0 {
		o.MinConfidence = 0.5
	}
	if o.SimilarityThreshold <= 0 {
		o.SimilarityThreshold = DefaultSimilarityThreshold
	}
	if o.MaxRecall <= 0 {
		o.MaxRecall = 5
	}
	o.CustomCategories = NormalizeCategories(o.CustomCategories)
	return o
}

// LocalAdaptive is the $0, on-device implementation of Adaptive.
type LocalAdaptive struct {
	store    *FactSQLite
	embedder Embedder // may be nil: keyword-only retrieval, rule-only resolution
	complete Completer
	opts     LocalOptions
}

// NewLocalAdaptive builds the local engine. embedder and complete may be nil;
// the engine degrades to keyword retrieval and rule-based resolution, and
// Remember becomes a no-op when there is no completer to extract with.
func NewLocalAdaptive(store *FactSQLite, embedder Embedder, complete Completer, opts LocalOptions) *LocalAdaptive {
	return &LocalAdaptive{store: store, embedder: embedder, complete: complete, opts: opts.withDefaults()}
}

// SetCompleter swaps the extraction/arbitration model (hot config changes).
func (l *LocalAdaptive) SetCompleter(c Completer) { l.complete = c }

// SetOptions swaps tuning knobs (hot config changes).
func (l *LocalAdaptive) SetOptions(o LocalOptions) { l.opts = o.withDefaults() }

// Store exposes the underlying fact store for host-side operations such as
// counts and exports.
func (l *LocalAdaptive) Store() *FactSQLite { return l.store }

func (l *LocalAdaptive) Provider() string { return "local" }

// Categories implements Adaptive.
func (l *LocalAdaptive) Categories() []string {
	out := make([]string, 0, len(BuiltinCategories)+len(l.opts.CustomCategories))
	for _, c := range BuiltinCategories {
		out = append(out, string(c))
	}
	return append(out, l.opts.CustomCategories...)
}

func (l *LocalAdaptive) Close() error {
	if l.store == nil {
		return nil
	}
	return l.store.Close()
}

func (l *LocalAdaptive) embed(ctx context.Context, text string) []float32 {
	if l.embedder == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	vec, err := l.embedder.Embed(ctx, text)
	if err != nil {
		return nil
	}
	return vec
}

func (l *LocalAdaptive) extractOptions() ExtractOptions {
	return ExtractOptions{Instructions: l.opts.Instructions, CustomCategories: l.opts.CustomCategories}
}

// Remember implements Adaptive. It never writes for turns that yield no
// candidates, and each candidate is reconciled independently so one bad
// candidate cannot block the others.
func (l *LocalAdaptive) Remember(ctx context.Context, scope FactScope, turn Turn) (RememberOutcome, error) {
	out := RememberOutcome{Provider: "local", ExtractedAt: time.Now().UTC()}
	scope = scope.Normalize()
	if scope.Owner == "" {
		return out, ErrInvalidScope
	}
	if l.complete == nil || !ShouldExtract(turn) {
		return out, nil
	}
	ext, err := Extract(ctx, l.complete, turn, l.extractOptions())
	if err != nil {
		return out, err
	}
	if l.opts.GraphDisabled {
		ext.Relations = nil
	}
	out.Candidates = len(ext.Facts) + len(ext.Relations)
	if out.Candidates == 0 {
		return out, nil
	}
	// Recall-eligible facts for this session are the comparison set; a
	// temporary fact from another session must not be superseded here.
	scope.SessionID = turn.SessionID
	active, err := l.store.Active(ctx, scope)
	if err != nil {
		return out, err
	}
	for _, c := range ext.Facts {
		if c.Confidence < l.opts.MinConfidence {
			out.Skipped++
			continue
		}
		cand := Fact{
			Workspace: scope.Workspace, Owner: scope.Owner, AgentID: scope.AgentID,
			Category: FactCategory(c.Category), Content: c.Fact, Confidence: c.Confidence,
			Source: "extracted", SourceSessionID: turn.SessionID, SourceRunID: turn.RunID,
			SessionScoped: c.Temporary,
		}
		cand.Embedding = l.embed(ctx, cand.Content)
		similar := l.similar(cand, active)
		res := ResolveCandidate(ctx, l.complete, cand, similar, l.opts.SimilarityThreshold)
		switch res.Decision {
		case DecisionNoop:
			out.Skipped++
		case DecisionRetract:
			if _, err := l.store.Retract(ctx, scope, res.Existing.ID, "extractor"); err != nil && !errors.Is(err, ErrFactNotFound) {
				return out, err
			}
			out.Retracted = append(out.Retracted, res.Existing.ID)
			active = replaceOrAppend(active, Fact{}, res.Existing.ID)
		case DecisionSupersede:
			saved, err := l.store.Supersede(ctx, scope, res.Existing.ID, cand)
			if err != nil {
				if errors.Is(err, ErrFactNotFound) {
					// The old fact vanished mid-run; store the new one plainly.
					if saved, err = l.store.Insert(ctx, cand); err == nil {
						out.Added = append(out.Added, saved.ID)
						active = replaceOrAppend(active, saved, "")
					}
					continue
				}
				return out, err
			}
			out.Superseded = append(out.Superseded, res.Existing.ID)
			out.Added = append(out.Added, saved.ID)
			active = replaceOrAppend(active, saved, res.Existing.ID)
		default:
			saved, err := l.store.Insert(ctx, cand)
			if err != nil {
				return out, err
			}
			out.Added = append(out.Added, saved.ID)
			active = replaceOrAppend(active, saved, "")
		}
	}
	for _, rc := range ext.Relations {
		_, created, err := l.store.UpsertRelation(ctx, scope, Relation{
			Subject: rc.Subject, Predicate: rc.Predicate, Object: rc.Object,
			Source: "extracted", SourceSessionID: turn.SessionID, SourceRunID: turn.RunID,
		})
		if err != nil {
			if errors.Is(err, ErrInvalidFact) {
				out.Skipped++
				continue
			}
			return out, err
		}
		if created {
			out.RelationsAdded++
		} else {
			out.Skipped++
		}
	}
	return out, nil
}

// similar scores a candidate against active facts using embeddings when both
// sides have them, falling back to keyword overlap so a missing embedder still
// catches exact restatements.
func (l *LocalAdaptive) similar(cand Fact, active []Fact) []ScoredFact {
	tokens := Tokenize(cand.Content)
	var out []ScoredFact
	for _, f := range active {
		var score float64
		if len(cand.Embedding) > 0 && len(f.Embedding) == len(cand.Embedding) {
			score = CosineSimilarity(cand.Embedding, f.Embedding)
		} else {
			score = KeywordOverlap(tokens, f.Content)
			if strings.EqualFold(f.Content, cand.Content) {
				score = 1
			}
		}
		if score > 0 {
			out = append(out, ScoredFact{Fact: f, Score: score})
		}
	}
	SortScored(out)
	return out
}

// replaceOrAppend removes removeID from active (if set) and prepends saved
// (if it has an ID).
func replaceOrAppend(active []Fact, saved Fact, removeID string) []Fact {
	out := active[:0]
	for _, f := range active {
		if removeID != "" && f.ID == removeID {
			continue
		}
		out = append(out, f)
	}
	if saved.ID == "" {
		return out
	}
	return append([]Fact{saved}, out...)
}

// Recall implements Adaptive with hybrid ranking over the scope's active facts.
func (l *LocalAdaptive) Recall(ctx context.Context, scope FactScope, query string, limit int) ([]ScoredFact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if limit <= 0 {
		limit = l.opts.MaxRecall
	}
	active, err := l.store.Active(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	var qvec []float32
	if strings.TrimSpace(query) != "" {
		qvec = l.embed(ctx, query)
	}
	return RankFacts(active, qvec, Tokenize(query), limit), nil
}

// Relations implements Adaptive.
func (l *LocalAdaptive) Relations(ctx context.Context, scope FactScope, query string, limit int) ([]Relation, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if limit <= 0 {
		limit = 3
	}
	rels, err := l.store.Relations(ctx, scope, FactStatusActive, 500)
	if err != nil || len(rels) == 0 {
		return nil, err
	}
	return RankRelations(rels, Tokenize(query), limit), nil
}

func (l *LocalAdaptive) List(ctx context.Context, scope FactScope, status string, limit int) ([]Fact, error) {
	return l.store.List(ctx, scope, status, limit)
}

func (l *LocalAdaptive) checkCategory(c FactCategory) error {
	if !AllowedCategory(string(c), l.opts.CustomCategories) {
		return ErrInvalidFact
	}
	return nil
}

func (l *LocalAdaptive) Add(ctx context.Context, scope FactScope, in FactInput) (Fact, error) {
	scope = scope.Normalize()
	if err := l.checkCategory(in.Category); err != nil {
		return Fact{}, err
	}
	f := Fact{Workspace: scope.Workspace, Owner: scope.Owner, AgentID: scope.AgentID, Category: in.Category, Content: in.Content, Confidence: 1, Source: "manual", ExpiresAt: in.ExpiresAt}
	f, err := prepareFact(f)
	if err != nil {
		return Fact{}, err
	}
	f.Embedding = l.embed(ctx, f.Content)
	return l.store.Insert(ctx, f)
}

func (l *LocalAdaptive) Update(ctx context.Context, scope FactScope, id string, in FactInput) (Fact, error) {
	if err := l.checkCategory(in.Category); err != nil {
		return Fact{}, err
	}
	content, err := NormalizeFactContent(in.Content)
	if err != nil {
		return Fact{}, err
	}
	in.Content = content
	return l.store.Update(ctx, scope, id, in, l.embed(ctx, content), "user")
}

func (l *LocalAdaptive) Delete(ctx context.Context, scope FactScope, id string) error {
	return l.store.Delete(ctx, scope, id, "user")
}

func (l *LocalAdaptive) History(ctx context.Context, scope FactScope, id string) ([]FactEvent, error) {
	return l.store.History(ctx, scope, id)
}

func (l *LocalAdaptive) Purge(ctx context.Context, scope FactScope) (int64, error) {
	return l.store.Purge(ctx, scope)
}
