package costs

import (
	"context"
	"time"
)

// SessionMetrics is the aggregated LLM usage for one session (run).
// Provider and Model reflect the most recent call in the session.
type SessionMetrics struct {
	SessionID    string    `json:"session_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	LLMCalls     int       `json:"llm_calls"`
	PromptTokens int       `json:"prompt_tokens"`
	CompTokens   int       `json:"comp_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	FirstCallAt  time.Time `json:"first_call_at"`
	LastCallAt   time.Time `json:"last_call_at"`
}

// SessionMetrics aggregates token usage for a single session_id across all
// agents. found is false when the session has no recorded usage.
func (s *Store) SessionMetrics(ctx context.Context, sessionID string) (SessionMetrics, bool, error) {
	m := SessionMetrics{SessionID: sessionID}

	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(comp_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM token_usage WHERE session_id = ?`, sessionID).
		Scan(&m.LLMCalls, &m.PromptTokens, &m.CompTokens, &m.TotalTokens, &m.CostUSD)
	if err != nil {
		return SessionMetrics{}, false, err
	}
	if m.LLMCalls == 0 {
		return SessionMetrics{}, false, nil
	}

	// Provider/model + last-call time from the most recent record.
	if err := s.db.QueryRowContext(ctx, `
		SELECT provider, model, created_at FROM token_usage
		WHERE session_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID).
		Scan(&m.Provider, &m.Model, &m.LastCallAt); err != nil {
		return SessionMetrics{}, false, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT created_at FROM token_usage
		WHERE session_id = ? ORDER BY created_at ASC, id ASC LIMIT 1`, sessionID).
		Scan(&m.FirstCallAt); err != nil {
		return SessionMetrics{}, false, err
	}
	return m, true, nil
}
