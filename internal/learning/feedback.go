package learning

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/redact"
)

// Feedback is explicit human evaluation of one model response. Rating is 1
// (helpful) or -1 (unhelpful). It is stored separately from generated learning
// proposals so raw preference signals never become agent instructions without
// review.
type Feedback struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	SessionID  string    `json:"session_id"`
	RunID      string    `json:"run_id"`
	ResponseID string    `json:"response_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	Rating     int       `json:"rating"`
	Comment    string    `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Store) feedbackPath() string { return s.path + ".feedback.jsonl" }

// AddFeedback upserts the caller's evaluation for a response. Re-clicking the
// opposite thumb changes the signal instead of double-counting it.
func (s *Store) AddFeedback(f Feedback) (Feedback, error) {
	f.AgentID = strings.TrimSpace(f.AgentID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.RunID = strings.TrimSpace(f.RunID)
	f.ResponseID = strings.TrimSpace(f.ResponseID)
	f.Comment = strings.TrimSpace(redact.Text(f.Comment))
	if f.AgentID == "" || f.SessionID == "" || f.RunID == "" {
		return Feedback{}, fmt.Errorf("learning feedback: agent_id, session_id, and run_id are required")
	}
	if f.Rating != 1 && f.Rating != -1 {
		return Feedback{}, fmt.Errorf("learning feedback: rating must be 1 or -1")
	}
	if len(f.Comment) > 2000 {
		return Feedback{}, fmt.Errorf("learning feedback: comment exceeds 2000 bytes")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadFeedbackLocked()
	if err != nil {
		return Feedback{}, err
	}
	now := time.Now().UTC()
	for i := range all {
		if all[i].AgentID == f.AgentID && all[i].RunID == f.RunID && all[i].ResponseID == f.ResponseID && all[i].UserID == f.UserID {
			f.ID = all[i].ID
			f.CreatedAt = all[i].CreatedAt
			f.UpdatedAt = now
			all[i] = f
			return f, s.writeFeedbackLocked(all)
		}
	}
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	f.CreatedAt, f.UpdatedAt = now, now
	all = append(all, f)
	return f, s.writeFeedbackLocked(all)
}

func (s *Store) ListFeedback(agentID string, limit int) ([]Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadFeedbackLocked()
	if err != nil {
		return nil, err
	}
	out := make([]Feedback, 0, len(all))
	for _, f := range all {
		if agentID == "" || f.AgentID == agentID {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) loadFeedbackLocked() ([]Feedback, error) {
	f, err := os.Open(s.feedbackPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("learning feedback: open: %w", err)
	}
	defer f.Close()
	var out []Feedback
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	for scan.Scan() {
		var item Feedback
		if json.Unmarshal(scan.Bytes(), &item) == nil {
			out = append(out, item)
		}
	}
	return out, scan.Err()
}

func (s *Store) writeFeedbackLocked(all []Feedback) error {
	const maxFeedbackRecords = 10_000
	if len(all) > maxFeedbackRecords {
		sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
		all = all[:maxFeedbackRecords]
	}
	tmp := s.feedbackPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("learning feedback: rewrite open: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, item := range all {
		if err := enc.Encode(item); err != nil {
			f.Close()
			return fmt.Errorf("learning feedback: encode: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.feedbackPath())
}
