package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/actionlog"
)

type sloCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type sloReadiness struct {
	Status              string                          `json:"status"`
	Score               int                             `json:"score"`
	Ready               int                             `json:"ready"`
	Total               int                             `json:"total"`
	Window              string                          `json:"window"`
	MaxFailureRate      float64                         `json:"max_failure_rate"`
	MaxIncompleteRate   float64                         `json:"max_incomplete_rate"`
	MaxP95RunDurationMS int64                           `json:"max_p95_run_duration_ms"`
	MinRunsForSignal    int                             `json:"min_runs_for_signal"`
	Summary             *sloSummaryView                 `json:"summary,omitempty"`
	Checks              []sloCheck                      `json:"checks"`
	NextActions         []string                        `json:"next_actions,omitempty"`
	TopFailingAgents    []actionlog.AgentFailureSummary `json:"top_failing_agents,omitempty"`
	RecentFailures      []actionlog.RunFailure          `json:"recent_failures,omitempty"`
}

type sloSummaryView struct {
	TotalRuns      int     `json:"total_runs"`
	SuccessfulRuns int     `json:"successful_runs"`
	FailedRuns     int     `json:"failed_runs"`
	IncompleteRuns int     `json:"incomplete_runs"`
	FailureRate    float64 `json:"failure_rate"`
	IncompleteRate float64 `json:"incomplete_rate"`
	AvgDurationMS  int64   `json:"avg_duration_ms"`
	P95DurationMS  int64   `json:"p95_duration_ms"`
	MaxDurationMS  int64   `json:"max_duration_ms"`
}

func (s *Server) handleSLOStatus(c *fiber.Ctx) error {
	return c.JSON(s.sloReadiness(c))
}

func (s *Server) sloReadiness(c *fiber.Ctx) sloReadiness {
	window := strings.TrimSpace(s.cfg.Ops.SLOWindow)
	if window == "" {
		window = "24h"
	}
	if q := strings.TrimSpace(c.Query("window")); q != "" {
		window = q
	}
	since, label, err := parseCostSince(window)
	if err != nil {
		label = "24h"
		since = time.Now().Add(-24 * time.Hour)
	}
	maxFailureRate := positiveOrDefault(s.cfg.Ops.MaxFailureRate, 0.10)
	maxIncompleteRate := positiveOrDefault(s.cfg.Ops.MaxIncompleteRate, 0.05)
	maxP95 := parseDurationOrDefault(s.cfg.Ops.MaxP95RunDuration, 5*time.Minute)
	minRuns := s.cfg.Ops.MinRunsForSignal
	if minRuns <= 0 {
		minRuns = 3
	}

	checks := []sloCheck{{
		Key:    "action_log",
		Label:  "Durable action log",
		Status: "fail",
		Detail: "Action log is not available; SLO checks cannot inspect recent runs.",
	}}
	var summary actionlog.OpsSummary
	if s.actions != nil {
		if sp, ok := s.actions.(opsSummarizer); ok {
			if got, err := sp.OpsSummary(since, label, 8); err == nil {
				summary = got
				checks[0].Status = "ok"
				checks[0].Detail = "Recent run outcomes are available from the durable action log."
			} else {
				checks[0].Detail = "Action log query failed: " + err.Error()
			}
		}
	}

	haveLog := checks[0].Status == "ok"
	sampleStatus := "fail"
	sampleDetail := "No recent run history is available."
	if haveLog {
		sampleStatus = statusIf(summary.TotalRuns >= minRuns, "ok", "warn")
		sampleDetail = fmt.Sprintf("%d run(s) in %s; target is at least %d before trusting the SLO signal.", summary.TotalRuns, label, minRuns)
	}
	checks = append(checks, sloCheck{
		Key:    "sample",
		Label:  "Enough signal",
		Status: sampleStatus,
		Detail: sampleDetail,
	})
	checks = append(checks, sloCheck{
		Key:    "failure_rate",
		Label:  "Failure rate",
		Status: rateStatus(haveLog, summary.TotalRuns, summary.FailureRate, maxFailureRate),
		Detail: fmt.Sprintf("%.1f%% failed; limit is %.1f%%.", summary.FailureRate*100, maxFailureRate*100),
	})
	checks = append(checks, sloCheck{
		Key:    "incomplete_rate",
		Label:  "Incomplete runs",
		Status: rateStatus(haveLog, summary.TotalRuns, summary.IncompleteRate, maxIncompleteRate),
		Detail: fmt.Sprintf("%.1f%% incomplete; limit is %.1f%%.", summary.IncompleteRate*100, maxIncompleteRate*100),
	})
	checks = append(checks, sloCheck{
		Key:    "p95_latency",
		Label:  "P95 run duration",
		Status: durationStatus(haveLog, summary.TotalRuns, time.Duration(summary.P95DurationMS)*time.Millisecond, maxP95),
		Detail: fmt.Sprintf("P95 is %s; limit is %s.", humanDurationMS(summary.P95DurationMS), maxP95),
	})

	ready, total := 0, len(checks)
	status := "ok"
	next := make([]string, 0)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
		case "warn":
			if status == "ok" {
				status = "warn"
			}
			next = append(next, sloNextAction(check.Key))
		default:
			status = "fail"
			next = append(next, sloNextAction(check.Key))
		}
	}
	score := 0
	if total > 0 {
		score = int(float64(ready) / float64(total) * 100)
	}
	out := sloReadiness{
		Status:              status,
		Score:               score,
		Ready:               ready,
		Total:               total,
		Window:              label,
		MaxFailureRate:      maxFailureRate,
		MaxIncompleteRate:   maxIncompleteRate,
		MaxP95RunDurationMS: maxP95.Milliseconds(),
		MinRunsForSignal:    minRuns,
		Checks:              checks,
		NextActions:         uniqueStrings(next),
		TopFailingAgents:    summary.TopFailing,
		RecentFailures:      summary.RecentFailures,
	}
	if haveLog {
		out.Summary = &sloSummaryView{
			TotalRuns:      summary.TotalRuns,
			SuccessfulRuns: summary.SuccessfulRuns,
			FailedRuns:     summary.FailedRuns,
			IncompleteRuns: summary.IncompleteRuns,
			FailureRate:    summary.FailureRate,
			IncompleteRate: summary.IncompleteRate,
			AvgDurationMS:  summary.AvgDurationMS,
			P95DurationMS:  summary.P95DurationMS,
			MaxDurationMS:  summary.MaxDurationMS,
		}
	}
	return out
}

func rateStatus(haveLog bool, totalRuns int, got, limit float64) string {
	if !haveLog {
		return "fail"
	}
	if totalRuns == 0 {
		return "warn"
	}
	if got > limit {
		return "fail"
	}
	return "ok"
}

func durationStatus(haveLog bool, totalRuns int, got, limit time.Duration) string {
	if !haveLog {
		return "fail"
	}
	if totalRuns == 0 || got <= 0 {
		return "warn"
	}
	if limit > 0 && got > limit {
		return "fail"
	}
	return "ok"
}

func positiveOrDefault(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && d > 0 {
		return d
	}
	return fallback
}

func humanDurationMS(ms int64) string {
	if ms <= 0 {
		return "not enough data"
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

func sloNextAction(key string) string {
	switch key {
	case "action_log":
		return "Enable durable action logging before production launch."
	case "sample":
		return "Run several representative agents so SLO status reflects real traffic."
	case "failure_rate":
		return "Open Activity, inspect top failing agents, and repair repeated errors in Studio."
	case "incomplete_rate":
		return "Review timeout and cancellation patterns; increase agent run_timeout only after checking provider latency."
	case "p95_latency":
		return "Tune model choice, executor backend, and slow tools until P95 run latency is within the configured limit."
	default:
		return "Review SLO settings and recent run history."
	}
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
