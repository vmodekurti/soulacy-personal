package person

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Render turns the model into prose for an agent's prompt.
//
// Agents get text, not JSON. A model rendered as JSON reaches the person as
// JSON sooner or later — that is exactly how the Phone brief once answered
// with a raw tool result — and it wastes tokens on syntax. The structured
// entries stay available to anything that needs the parts.
func Render(model Model, now time.Time) string {
	if model.Empty() {
		return "Nothing is known about this person yet. Ask, or let the observers run."
	}
	var b strings.Builder
	for _, section := range Sections {
		rows := model.Sections[section]
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", sectionHeading(section))
		for _, entry := range rows {
			fmt.Fprintf(&b, "- %s%s\n", entry.Summary, qualifier(entry, now))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func sectionHeading(section Section) string {
	switch section {
	case SectionIdentity:
		return "Who they are"
	case SectionRoutine:
		return "Their usual day"
	case SectionState:
		return "Right now"
	case SectionRelationships:
		return "People who matter"
	case SectionCommitments:
		return "Open commitments"
	case SectionPreferences:
		return "Preferences"
	default:
		return strings.ToUpper(string(section[:1])) + string(section[1:])
	}
}

// qualifier appends the caveats an agent must see: how sure we are, how old
// it is, and whether the person said it themselves. An agent that cannot
// tell a guess from a statement will state guesses as fact.
func qualifier(entry Entry, now time.Time) string {
	var parts []string
	switch {
	case entry.Origin() == OriginManual:
		parts = append(parts, "they told us")
	case entry.Quoted():
		// An agent that can quote the person is not inferring. Calling this
		// an inference would understate it and invite an agent to hedge
		// about something the person stated plainly.
		parts = append(parts, "they told us")
	case entry.Origin() == OriginAgent:
		parts = append(parts, "inferred by "+strings.TrimPrefix(entry.Source, SourceAgentPrefix))
	default:
		if name := strings.TrimPrefix(entry.Source, SourceSensePrefix); name != entry.Source {
			parts = append(parts, "from "+name)
		}
	}
	if entry.Origin() != OriginManual && !entry.Quoted() && entry.Confidence < 0.7 {
		parts = append(parts, "low confidence")
	}
	if age := now.Sub(entry.ObservedAt); age > 36*time.Hour && !entry.ObservedAt.IsZero() {
		parts = append(parts, "as of "+entry.ObservedAt.Format("2 Jan"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// RenderSections renders only the sections asked for, which is what an agent
// that only needs "right now" should pay for.
func RenderSections(model Model, sections []Section, now time.Time) string {
	if len(sections) == 0 {
		return Render(model, now)
	}
	wanted := map[Section][]Entry{}
	for _, section := range sections {
		if rows, ok := model.Sections[section]; ok {
			wanted[section] = rows
		}
	}
	return Render(Model{Owner: model.Owner, Sections: wanted, UpdatedAt: model.UpdatedAt}, now)
}

// Search returns the entries whose summary or key matches every word of the
// query, best first. Deliberately a word match rather than embeddings: the
// model is small, and an agent asking "what does Priya need" should not be
// answered with something that merely feels similar.
func Search(model Model, query string, limit int) []Entry {
	words := searchWords(query)
	if len(words) == 0 {
		return nil
	}
	type scored struct {
		entry Entry
		score int
	}
	var hits []scored
	for _, rows := range model.Sections {
		for _, entry := range rows {
			haystack := strings.ToLower(entry.Summary + " " + entry.Key)
			score := 0
			for _, word := range words {
				if strings.Contains(haystack, word) {
					score++
				}
			}
			if score == len(words) {
				hits = append(hits, scored{entry: entry, score: score})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].entry.UpdatedAt.After(hits[j].entry.UpdatedAt)
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	entries := make([]Entry, 0, len(hits))
	for _, hit := range hits {
		entries = append(entries, hit.entry)
	}
	return entries
}

func searchWords(query string) []string {
	var words []string
	for _, word := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	}) {
		if len(word) > 2 {
			words = append(words, word)
		}
	}
	return words
}
