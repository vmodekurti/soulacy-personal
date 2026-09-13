package learning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/soulacy/soulacy/internal/injection"
	"github.com/soulacy/soulacy/internal/redact"
)

var (
	ErrInvalidLesson  = errors.New("learning: invalid lesson")
	ErrLessonConflict = errors.New("learning: lesson changed; refresh and review the current version")
	ErrLessonNotFound = errors.New("learning: lesson not found")
	ErrNotebookFull   = errors.New("learning: notebook capacity reached; review pending drafts, active lessons and retained-history limits")
)

const MaxLessonRequest = 32 * 1024

// Scope is supplied by authentication and the runtime, never by model output.
// Even administrators have separate private notebooks on a shared agent.
type Scope struct {
	Owner   string
	AgentID string
}

type Source struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // user or tool; assistant prose is not evidence
	Text   string `json:"text"`
	Tool   string `json:"tool,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

type Citation struct {
	SourceID string `json:"source_id"`
	Quote    string `json:"quote"`
}

// Draft represents one reusable idea, not a transcript or an executable tool.
// Key is stable across corrections; BaseID makes refinement compare-and-swap.
type Draft struct {
	Key          string     `json:"key"`
	Kind         string     `json:"kind"` // preference, fact, skill
	Title        string     `json:"title"`
	Trigger      string     `json:"trigger"`
	Content      string     `json:"content"`
	Verification string     `json:"verification"`
	Pitfalls     string     `json:"pitfalls,omitempty"`
	BaseID       string     `json:"base_id,omitempty"`
	Citations    []Citation `json:"citations"`
}

type Lesson struct {
	Draft
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	RunID     string    `json:"run_id"`
	SessionID string    `json:"session_id,omitempty"`
	Sources   []Source  `json:"sources"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Uses      int       `json:"uses"`
	Helpful   int       `json:"helpful"`
	Unhelpful int       `json:"unhelpful"`
}

type Episode struct {
	RunID     string    `json:"run_id"`
	SessionID string    `json:"session_id"`
	Request   string    `json:"request"`
	Reply     string    `json:"reply"`
	Success   bool      `json:"success"`
	CreatedAt time.Time `json:"created_at"`
}

var lessonKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func validScope(s Scope) bool { return bounded(s.Owner, 256) && bounded(s.AgentID, 128) }
func bounded(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// DecodeLessonRequest rejects duplicate keys, case aliases, unknown fields,
// trailing JSON and invalid Unicode. Approval bodies must have one meaning.
func DecodeLessonRequest(raw []byte, v any) error {
	if len(raw) == 0 || len(raw) > MaxLessonRequest || !utf8.Valid(raw) {
		return ErrInvalidLesson
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrInvalidLesson
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 12 {
			return ErrInvalidLesson
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || key != strings.ToLower(key) || seen[key] {
					return ErrInvalidLesson
				}
				seen[key] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return nil
		}
	}
	if err := walk(0); err != nil {
		return ErrInvalidLesson
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalidLesson
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ErrInvalidLesson
	}
	return nil
}

func safeLessonText(s string) bool {
	if !utf8.ValidString(s) || redact.Text(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n' && r != '\t') || r == utf8.RuneError {
			return false
		}
	}
	return !injection.Scan(s).HasHighSeverity()
}

func validateDraft(d Draft, sources []Source) ([]Source, error) {
	if !lessonKey.MatchString(d.Key) {
		return nil, fmt.Errorf("%w: key must be a lowercase hyphenated topic slug, 1-64 characters; no underscores", ErrInvalidLesson)
	}
	if len(d.Citations) < 1 || len(d.Citations) > 6 {
		return nil, fmt.Errorf("%w: provide 1-6 citations with source_id and an exact quote (8-600 bytes). Current user message is source_id user; learning.search lists tool sources", ErrInvalidLesson)
	}
	if !lessonKey.MatchString(d.Key) || (d.Kind != "preference" && d.Kind != "fact" && d.Kind != "skill") ||
		!bounded(d.Title, 120) || !bounded(d.Trigger, 400) || !bounded(d.Content, 6000) ||
		!bounded(d.Verification, 1000) || len(d.Pitfalls) > 1500 || len(d.BaseID) > 128 || len(d.Citations) < 1 || len(d.Citations) > 6 {
		return nil, ErrInvalidLesson
	}
	if d.Kind != "skill" && len(d.Content) > 1200 {
		return nil, fmt.Errorf("%w: facts and preferences must be concise", ErrInvalidLesson)
	}
	if !safeLessonText(strings.Join([]string{d.Title, d.Trigger, d.Content, d.Verification, d.Pitfalls}, "\n")) {
		return nil, fmt.Errorf("%w: remove credentials, hidden text, or instruction overrides", ErrInvalidLesson)
	}
	refs := make([]Source, 0, len(d.Citations))
	seen := map[string]bool{}
	for _, c := range d.Citations {
		if !bounded(c.Quote, 600) || len(strings.TrimSpace(c.Quote)) < 8 || !safeLessonText(c.Quote) || seen[c.SourceID+"\x00"+c.Quote] {
			return nil, ErrInvalidLesson
		}
		seen[c.SourceID+"\x00"+c.Quote] = true
		found := false
		for _, src := range sources {
			if src.ID != c.SourceID || !strings.Contains(src.Text, c.Quote) {
				continue
			}
			if src.Kind != "user" && src.Kind != "tool" {
				continue
			}
			// Only the person can establish their preferences. A tool result or
			// an assistant's confident answer cannot invent a user profile.
			if d.Kind == "preference" && src.Kind != "user" {
				continue
			}
			if d.Kind == "fact" && src.Failed {
				continue
			}
			src.Text = c.Quote
			refs = append(refs, src)
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("%w: citation must quote an observed user message or tool result", ErrInvalidLesson)
		}
	}
	return refs, nil
}

func Clip(s string, max int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func normalized(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func lessonTerms(s string) map[string]bool {
	terms := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(Clip(s, 8000)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(w) < 2 || strings.Contains(" a an the and or to of for in is it my me please this that with from how what when ", " "+w+" ") {
			continue
		}
		terms[w] = true
		if len(terms) >= 64 {
			break
		}
	}
	return terms
}

func termScore(query map[string]bool, text string) int {
	words := lessonTerms(text)
	n := 0
	for w := range query {
		if words[w] {
			n++
		}
	}
	return n
}
