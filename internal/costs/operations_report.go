package costs

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type OperationsUsageTotals struct {
	Requests      int64 `json:"requests"`
	Attempts      int64 `json:"attempts"`
	Failed        int64 `json:"failed"`
	Rejected      int64 `json:"rejected"`
	UnknownPriced int64 `json:"unknown_priced"`
	TotalTokens   int64 `json:"total_tokens"`
	CostMicros    int64 `json:"cost_micros"`
}

type OperationsUsageRow struct {
	AgentID  string `json:"agent_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	OperationsUsageTotals
}

type OperationsUsage struct {
	Totals  OperationsUsageTotals `json:"totals"`
	ByAgent []OperationsUsageRow  `json:"by_agent"`
	ByModel []OperationsUsageRow  `json:"by_model"`
}

// OperationsUsage reports only accounting aggregates. It never exports user
// identities, provider request IDs, session IDs, or free-form error strings.
// All totals/breakdowns derive from one query snapshot, not separately sampled
// queries that could disagree while an agent is running.
func (s *Store) OperationsUsage(ctx context.Context, start, end time.Time) (OperationsUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, provider, model, COUNT(*),
		COALESCE(SUM(attempt_count), 0),
		SUM(CASE WHEN status NOT IN ('', 'success', 'rejected') THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'rejected' THEN 1 ELSE 0 END),
		SUM(CASE WHEN pricing_status IN ('', 'unknown') AND status <> 'rejected' THEN 1 ELSE 0 END),
		COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(CASE WHEN cost_micros = 0 AND cost_usd <> 0 THEN CAST(ROUND(cost_usd * 1000000) AS INTEGER) ELSE cost_micros END), 0)
		FROM token_usage WHERE created_at >= ? AND created_at < ?
		GROUP BY agent_id, provider, model LIMIT 10001`, start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return OperationsUsage{}, err
	}
	defer rows.Close()
	out := OperationsUsage{ByAgent: []OperationsUsageRow{}, ByModel: []OperationsUsageRow{}}
	agents := map[string]*OperationsUsageRow{}
	type modelKey struct{ provider, model string }
	models := map[modelKey]*OperationsUsageRow{}
	groups := 0
	for rows.Next() {
		groups++
		if groups > 10000 {
			return OperationsUsage{}, fmt.Errorf("operations usage exceeds 10000 groups; select a shorter window")
		}
		var row OperationsUsageRow
		if err := rows.Scan(&row.AgentID, &row.Provider, &row.Model, &row.Requests, &row.Attempts,
			&row.Failed, &row.Rejected, &row.UnknownPriced, &row.TotalTokens, &row.CostMicros); err != nil {
			return OperationsUsage{}, err
		}
		a := agents[row.AgentID]
		if a == nil {
			a = &OperationsUsageRow{AgentID: row.AgentID}
			agents[row.AgentID] = a
		}
		key := modelKey{row.Provider, row.Model}
		m := models[key]
		if m == nil {
			m = &OperationsUsageRow{Provider: row.Provider, Model: row.Model}
			models[key] = m
		}
		a.OperationsUsageTotals.add(row.OperationsUsageTotals)
		m.OperationsUsageTotals.add(row.OperationsUsageTotals)
		out.Totals.add(row.OperationsUsageTotals)
	}
	if err := rows.Err(); err != nil {
		return OperationsUsage{}, err
	}
	for _, row := range agents {
		out.ByAgent = append(out.ByAgent, *row)
	}
	for _, row := range models {
		out.ByModel = append(out.ByModel, *row)
	}
	sort.Slice(out.ByAgent, func(i, j int) bool {
		a, b := out.ByAgent[i], out.ByAgent[j]
		if a.CostMicros != b.CostMicros {
			return a.CostMicros > b.CostMicros
		}
		return a.AgentID < b.AgentID
	})
	sort.Slice(out.ByModel, func(i, j int) bool {
		a, b := out.ByModel[i], out.ByModel[j]
		if a.CostMicros != b.CostMicros {
			return a.CostMicros > b.CostMicros
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return out, nil
}

func (t *OperationsUsageTotals) add(v OperationsUsageTotals) {
	t.Requests += v.Requests
	t.Attempts += v.Attempts
	t.Failed += v.Failed
	t.Rejected += v.Rejected
	t.UnknownPriced += v.UnknownPriced
	t.TotalTokens += v.TotalTokens
	t.CostMicros += v.CostMicros
}
