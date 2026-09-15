// Package person holds the person model: Soulacy's structured understanding
// of the human it works for.
//
// Adaptive memory answers "what do I know about this person?" as a bag of
// sentences. The person model answers "what is true about this person right
// now?" in a shape agents and observers agree on: routines, current state,
// relationships, commitments and preferences. Observers (small digesters of
// sensor data) write it; agents read it through one tool instead of calling
// a dozen device commands and re-deriving the same context every run.
package person

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Section is one part of the model. Sections are independently readable and
// updatable so an observer that only knows about calendars never touches
// what the location observer wrote.
type Section string

const (
	// SectionIdentity is who the person is: name, pronouns, timezone, the
	// places that count as home and work.
	SectionIdentity Section = "identity"
	// SectionRoutine is the shape of a normal day, per weekday, and how far
	// today departs from it.
	SectionRoutine Section = "routine"
	// SectionState is now: awake, commuting, in focus, unwell, travelling.
	SectionState Section = "state"
	// SectionRelationships is who matters, how they are reached, and what is
	// outstanding with each of them.
	SectionRelationships Section = "relationships"
	// SectionCommitments is what the person owes and is owed, with due dates.
	SectionCommitments Section = "commitments"
	// SectionPreferences is what they like, learned from what they accepted
	// and rejected.
	SectionPreferences Section = "preferences"
)

// Sections lists every valid section, in the order a human would read them.
var Sections = []Section{
	SectionIdentity, SectionRoutine, SectionState,
	SectionRelationships, SectionCommitments, SectionPreferences,
}

// Valid reports whether s is a known section.
func (s Section) Valid() bool {
	for _, known := range Sections {
		if s == known {
			return true
		}
	}
	return false
}

// Origin ranks where an entry came from. Precedence is the whole point: a
// sensor may not overwrite what the person told us, and an agent's inference
// may not overwrite either. Higher wins.
type Origin int

const (
	// OriginSense is a device signal digested by an observer.
	OriginSense Origin = iota
	// OriginAgent is something an agent inferred during a run.
	OriginAgent
	// OriginManual is what the person typed, confirmed or corrected.
	OriginManual
)

// Source prefixes. A source is either "manual", "agent:<id>" or
// "sense:<name>"; the prefix decides precedence and the suffix is kept so the
// UI can say exactly which sense or agent is responsible.
const (
	SourceManual      = "manual"
	SourceAgentPrefix = "agent:"
	SourceSensePrefix = "sense:"
)

// OriginOf classifies a source string. An unrecognised source is treated as
// a sense: the least authority, so a bug cannot silently outrank the person.
func OriginOf(source string) Origin {
	switch s := strings.TrimSpace(strings.ToLower(source)); {
	case s == SourceManual:
		return OriginManual
	case strings.HasPrefix(s, SourceAgentPrefix):
		return OriginAgent
	default:
		return OriginSense
	}
}

// Entry is one fact in one section, keyed so repeated observations update
// rather than accumulate.
type Entry struct {
	Owner   string  `json:"owner"`
	Section Section `json:"section"`
	// Key identifies the entry within its section and is stable across
	// observations: "home", "weekday.leave", "priya-s", "seat-preference".
	Key string `json:"key"`
	// Summary is one line of prose. Agents read this; the structured Value
	// is there for anything that needs the parts.
	Summary string `json:"summary"`
	// Value carries the structured form. Free-shaped per section on purpose:
	// observers evolve faster than a migration would allow.
	Value map[string]any `json:"value,omitempty"`
	// Source is "manual", "agent:<id>" or "sense:<name>".
	Source string `json:"source"`
	// Confidence is 0..1. Manual entries are always 1.
	Confidence float32 `json:"confidence"`
	// ObservedAt is when the underlying signal happened, which is not always
	// when it was written.
	ObservedAt time.Time  `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Origin is the entry's precedence rank.
func (e Entry) Origin() Origin { return OriginOf(e.Source) }

// Quoted reports whether the entry carries the person's own words. An agent
// may only attach a quote the person actually said, so a quoted entry counts
// as something they told us rather than something an agent concluded.
func (e Entry) Quoted() bool {
	said, ok := e.Value["said"].(string)
	return ok && strings.TrimSpace(said) != ""
}

// Expired reports whether the entry should no longer be read. State entries
// especially are short-lived: "commuting" is wrong an hour later.
func (e Entry) Expired(now time.Time) bool {
	return e.ExpiresAt != nil && !e.ExpiresAt.After(now)
}

// Errors returned by a Store.
var (
	ErrInvalidEntry = errors.New("person: invalid entry")
	ErrInvalidOwner = errors.New("person: owner is required")
	ErrNotFound     = errors.New("person: entry not found")
)

// Limits chosen so the whole model stays readable in one prompt.
const (
	MaxKeyLength     = 120
	MaxSummaryLength = 400
	MaxEntriesPerOwn = 2000
)

// Normalize trims, lower-cases the key, and fills in defaults. It is called
// by the store before validation so callers need not be careful.
func (e Entry) Normalize(now time.Time) Entry {
	e.Owner = strings.TrimSpace(e.Owner)
	e.Key = strings.ToLower(strings.TrimSpace(e.Key))
	e.Summary = strings.TrimSpace(e.Summary)
	e.Source = strings.TrimSpace(e.Source)
	if e.Source == "" {
		e.Source = SourceManual
	}
	if e.Origin() == OriginManual {
		e.Confidence = 1
	}
	if e.Confidence <= 0 {
		e.Confidence = 0.6
	}
	if e.Confidence > 1 {
		e.Confidence = 1
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = now
	}
	e.ObservedAt = e.ObservedAt.UTC()
	if e.ExpiresAt != nil {
		expires := e.ExpiresAt.UTC()
		e.ExpiresAt = &expires
	}
	return e
}

// Validate rejects entries that would make the model unreadable or unkeyed.
func (e Entry) Validate() error {
	if e.Owner == "" {
		return ErrInvalidOwner
	}
	if !e.Section.Valid() {
		return fmt.Errorf("%w: unknown section %q", ErrInvalidEntry, e.Section)
	}
	if e.Key == "" || len(e.Key) > MaxKeyLength {
		return fmt.Errorf("%w: key must be 1-%d characters", ErrInvalidEntry, MaxKeyLength)
	}
	if e.Summary == "" || len(e.Summary) > MaxSummaryLength {
		return fmt.Errorf("%w: summary must be 1-%d characters", ErrInvalidEntry, MaxSummaryLength)
	}
	return nil
}

// PutResult says what the store did. An observer whose write was outranked
// is told so rather than failing: being overruled by the person is normal.
type PutResult struct {
	// Entry is what is stored now, which is the existing entry when the
	// write was outranked.
	Entry Entry `json:"entry"`
	// Applied is false when an entry of higher origin already held the key.
	Applied bool `json:"applied"`
	// Reason explains a refusal in words the UI can show.
	Reason string `json:"reason,omitempty"`
}

// Outranks reports whether an incoming entry may replace an existing one.
// Equal origins replace (a newer observation of the same kind wins), except
// that an older observation never replaces a newer one.
func Outranks(incoming, existing Entry) bool {
	in, ex := incoming.Origin(), existing.Origin()
	if in != ex {
		return in > ex
	}
	return !incoming.ObservedAt.Before(existing.ObservedAt)
}

// Model is the whole thing for one owner, grouped by section.
type Model struct {
	Owner     string              `json:"owner"`
	Sections  map[Section][]Entry `json:"sections"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// NewModel groups entries into a model, dropping expired ones and sorting
// each section by key so output is stable.
func NewModel(owner string, entries []Entry, now time.Time) Model {
	model := Model{Owner: owner, Sections: map[Section][]Entry{}}
	for _, entry := range entries {
		if entry.Expired(now) {
			continue
		}
		model.Sections[entry.Section] = append(model.Sections[entry.Section], entry)
		if entry.UpdatedAt.After(model.UpdatedAt) {
			model.UpdatedAt = entry.UpdatedAt
		}
	}
	for section := range model.Sections {
		rows := model.Sections[section]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
		model.Sections[section] = rows
	}
	return model
}

// Empty reports whether anything is known at all.
func (m Model) Empty() bool {
	for _, rows := range m.Sections {
		if len(rows) > 0 {
			return false
		}
	}
	return true
}
