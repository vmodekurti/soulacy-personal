// Package learning stores reviewable learning proposals produced after runs.
package learning

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusRejected = "rejected"
)

// Proposal is a candidate memory, preference, procedure, or skill extracted
// from a completed task. It is intentionally reviewable by default.
type Proposal struct {
	ID         string            `json:"id"`
	AgentID    string            `json:"agent_id"`
	SessionID  string            `json:"session_id,omitempty"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	Content    string            `json:"content"`
	Status     string            `json:"status"`
	Confidence float64           `json:"confidence"`
	Source     string            `json:"source,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	// Disabled turns off an already-accepted learning without deleting it, so it
	// stops affecting agents but its review history is preserved (Epic 10).
	Disabled bool `json:"disabled,omitempty"`
	// Rationale is an optional explicit "why this matters" note. When empty, the
	// UI falls back to Why() derived from the kind/metadata.
	Rationale string `json:"rationale,omitempty"`
	// PromoteToStudioLessons opts an accepted proposal into Studio's
	// generation-prompt lesson injection (see internal/studio/lessons.go).
	// nil = use kind-based default (skill = true, everything else = false).
	// Story 8 AC3 — unifies Brain Memory learning and Studio lesson store so
	// operators see one lesson surface. See PROMOTIONAL_LESSONS in learning.go.
	PromoteToStudioLessons *bool `json:"promote_to_studio_lessons,omitempty"`
}

// EffectivePromoteToStudioLessons resolves the per-proposal opt-in flag,
// falling back to the kind-based default when the operator hasn't chosen:
// skills promote by default (they're the reusable procedure surface Studio
// needs), procedural and semantic learnings don't (they're agent-specific
// runtime rules, not shape/format facts about tools).
func (p Proposal) EffectivePromoteToStudioLessons() bool {
	if p.PromoteToStudioLessons != nil {
		return *p.PromoteToStudioLessons
	}
	return strings.EqualFold(strings.TrimSpace(p.Kind), "skill")
}

// Why returns a plain-language reason the learning matters — the explicit
// Rationale when set, otherwise one derived from the kind and metadata.
func (p Proposal) Why() string {
	if s := strings.TrimSpace(p.Rationale); s != "" {
		return s
	}
	switch p.Kind {
	case "skill":
		name := ""
		if p.Meta != nil {
			name = p.Meta["skill_name"]
		}
		if name != "" {
			return "Reusable skill \"" + name + "\" — lets the agent repeat this procedure without re-deriving it each time."
		}
		return "A reusable skill the agent can apply again instead of re-deriving the steps."
	case "procedure":
		return "An operating rule that reduces repeated mistakes on this kind of task."
	case "preference":
		return "A remembered preference so the agent doesn't have to ask again."
	default:
		if p.Confidence >= 0.8 {
			return "A high-confidence lesson learned from a completed run."
		}
		return "A lesson extracted from a completed run, kept for review."
	}
}

// SetDisabled toggles the Disabled flag on a learning. Unlike editing
// (pending-only), disabling is allowed on accepted proposals so a bad learning
// can be switched off without destroying unrelated memory.
func (s *Store) SetDisabled(id string, disabled bool) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Proposal{}, err
	}
	for i := range all {
		if all[i].ID != id {
			continue
		}
		all[i].Disabled = disabled
		all[i].UpdatedAt = time.Now().UTC()
		if err := s.rewriteLocked(all); err != nil {
			return Proposal{}, err
		}
		return all[i], nil
	}
	return Proposal{}, os.ErrNotExist
}

// Summary is the product-facing health snapshot for the learning loop. It lets
// the UI show whether proposals are piling up, being accepted, and turning into
// reusable procedures or skills.
type Summary struct {
	AgentID           string         `json:"agent_id,omitempty"`
	Total             int            `json:"total"`
	Pending           int            `json:"pending"`
	Accepted          int            `json:"accepted"`
	Rejected          int            `json:"rejected"`
	Memories          int            `json:"memories"`
	Procedures        int            `json:"procedures"`
	Skills            int            `json:"skills"`
	InstalledSkills   int            `json:"installed_skills"`
	BackgroundRuns    int            `json:"background_runs"`
	ManualReviews     int            `json:"manual_reviews"`
	AverageConfidence float64        `json:"average_confidence"`
	LatestAt          *time.Time     `json:"latest_at,omitempty"`
	LatestBackground  *time.Time     `json:"latest_background,omitempty"`
	BySource          map[string]int `json:"by_source,omitempty"`
	ByTool            map[string]int `json:"by_tool,omitempty"`
}

// Store persists proposals as JSONL. The write path is guarded by a mutex and
// status updates rewrite the compacted file; proposal volume is expected to be
// modest and human-reviewed.
type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("learning: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("learning: mkdir: %w", err)
	}
	return &Store{path: path}, nil
}

func (s *Store) Add(p Proposal) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p.Content = strings.TrimSpace(p.Content)
	if p.AgentID == "" || p.Content == "" {
		return Proposal{}, fmt.Errorf("learning: agent_id and content are required")
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Kind == "" {
		p.Kind = "memory"
	}
	if p.Status == "" {
		p.Status = StatusPending
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	if p.Confidence == 0 {
		p.Confidence = 0.6
	}
	if p.Meta == nil {
		p.Meta = map[string]string{}
	}
	p.Meta["dedupe"] = dedupeKey(p)

	existing, err := s.loadLocked()
	if err != nil {
		return Proposal{}, err
	}
	for _, prev := range existing {
		if prev.Status == StatusPending && prev.Meta["dedupe"] == p.Meta["dedupe"] {
			return prev, nil
		}
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return Proposal{}, fmt.Errorf("learning: open: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(p)
	if err != nil {
		return Proposal{}, fmt.Errorf("learning: marshal: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return Proposal{}, fmt.Errorf("learning: append: %w", err)
	}
	return p, nil
}

func (s *Store) List(agentID, status string, limit int) ([]Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for _, p := range all {
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) Summary(agentID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Summary{}, err
	}
	out := Summary{
		AgentID:  agentID,
		BySource: map[string]int{},
		ByTool:   map[string]int{},
	}
	var confSum float64
	for _, p := range all {
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		out.Total++
		confSum += p.Confidence
		switch p.Status {
		case StatusPending:
			out.Pending++
		case StatusAccepted:
			out.Accepted++
		case StatusRejected:
			out.Rejected++
		}
		switch strings.ToLower(p.Kind) {
		case "skill":
			out.Skills++
			if strings.TrimSpace(p.Meta["installed_path"]) != "" {
				out.InstalledSkills++
			}
		case "procedure":
			out.Procedures++
		default:
			out.Memories++
		}
		if src := strings.TrimSpace(p.Source); src != "" {
			out.BySource[src]++
			switch src {
			case "background_reflection":
				out.BackgroundRuns++
				if out.LatestBackground == nil || p.CreatedAt.After(*out.LatestBackground) {
					t := p.CreatedAt
					out.LatestBackground = &t
				}
			case "manual_run_review", "reflection_sweep":
				out.ManualReviews++
			}
		}
		if p.Meta["background_reflection"] == "true" && strings.TrimSpace(p.Source) != "background_reflection" {
			out.BackgroundRuns++
			if out.LatestBackground == nil || p.CreatedAt.After(*out.LatestBackground) {
				t := p.CreatedAt
				out.LatestBackground = &t
			}
		}
		for _, tool := range strings.Split(p.Meta["tools_used"], ",") {
			if tool = strings.TrimSpace(tool); tool != "" {
				out.ByTool[tool]++
			}
		}
		if out.LatestAt == nil || p.CreatedAt.After(*out.LatestAt) {
			t := p.CreatedAt
			out.LatestAt = &t
		}
	}
	if out.Total > 0 {
		out.AverageConfidence = confSum / float64(out.Total)
	}
	if len(out.BySource) == 0 {
		out.BySource = nil
	}
	if len(out.ByTool) == 0 {
		out.ByTool = nil
	}
	return out, nil
}

func (s *Store) UpdateStatus(id, status string) (Proposal, error) {
	return s.UpdateStatusMeta(id, status, nil)
}

// UpdateDraft edits a pending proposal before it is accepted or rejected.
// Accepted/rejected proposals are immutable so their review audit trail stays
// meaningful.
func (s *Store) UpdateDraft(id, title, content string, meta map[string]string) (Proposal, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Proposal{}, fmt.Errorf("learning: content is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Proposal{}, err
	}
	for i := range all {
		if all[i].ID != id {
			continue
		}
		if all[i].Status != StatusPending {
			return Proposal{}, fmt.Errorf("learning: only pending proposals can be edited")
		}
		all[i].Title = strings.TrimSpace(title)
		all[i].Content = content
		if all[i].Meta == nil {
			all[i].Meta = map[string]string{}
		}
		for k, v := range meta {
			if strings.TrimSpace(k) == "" {
				continue
			}
			all[i].Meta[k] = v
		}
		all[i].Meta["dedupe"] = dedupeKey(all[i])
		all[i].UpdatedAt = time.Now().UTC()
		if err := s.rewriteLocked(all); err != nil {
			return Proposal{}, err
		}
		return all[i], nil
	}
	return Proposal{}, os.ErrNotExist
}

// UpdateStatusMeta updates a proposal's review status and merges any supplied
// metadata into the stored record.
func (s *Store) UpdateStatusMeta(id, status string, meta map[string]string) (Proposal, error) {
	if status != StatusPending && status != StatusAccepted && status != StatusRejected {
		return Proposal{}, fmt.Errorf("learning: invalid status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Proposal{}, err
	}
	for i := range all {
		if all[i].ID != id {
			continue
		}
		if all[i].Meta == nil {
			all[i].Meta = map[string]string{}
		}
		for k, v := range meta {
			all[i].Meta[k] = v
		}
		all[i].Status = status
		all[i].UpdatedAt = time.Now().UTC()
		if err := s.rewriteLocked(all); err != nil {
			return Proposal{}, err
		}
		return all[i], nil
	}
	return Proposal{}, os.ErrNotExist
}

func (s *Store) loadLocked() ([]Proposal, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("learning: open: %w", err)
	}
	defer f.Close()

	var out []Proposal
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var p Proposal
		if err := json.Unmarshal(scanner.Bytes(), &p); err == nil && p.ID != "" {
			out = append(out, p)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("learning: scan: %w", err)
	}
	return out, nil
}

func (s *Store) rewriteLocked(all []Proposal) error {
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("learning: rewrite open: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, p := range all {
		if err := enc.Encode(p); err != nil {
			_ = f.Close()
			return fmt.Errorf("learning: rewrite encode: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("learning: rewrite close: %w", err)
	}
	return os.Rename(tmp, s.path)
}

func dedupeKey(p Proposal) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		p.AgentID,
		p.SessionID,
		p.Kind,
		strings.ToLower(strings.TrimSpace(p.Content)),
	}, "\x00")))
	return hex.EncodeToString(h[:])
}
