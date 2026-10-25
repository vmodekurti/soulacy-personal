package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// CaseComparison is the stable per-case delta between a saved baseline and a
// new candidate run.
type CaseComparison struct {
	Name            string `json:"name"`
	BaselineStatus  string `json:"baseline_status"`
	CandidateStatus string `json:"candidate_status"`
	LatencyDeltaMS  int64  `json:"latency_delta_ms"`
	TokenDelta      int    `json:"token_delta"`
	Regression      bool   `json:"regression,omitempty"`
	Improvement     bool   `json:"improvement,omitempty"`
}

// Comparison summarizes behavioral, latency, and token changes.
type Comparison struct {
	Baseline     Summary          `json:"baseline"`
	Candidate    Summary          `json:"candidate"`
	Cases        []CaseComparison `json:"cases"`
	Regressions  int              `json:"regressions"`
	Improvements int              `json:"improvements"`
	Added        int              `json:"added"`
	Missing      int              `json:"missing"`
}

// LoadResults parses a JSON result snapshot written by sy eval --write-results.
func LoadResults(data []byte) ([]Result, error) {
	var results []Result
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse eval results: %w", err)
	}
	return results, nil
}

// Compare matches cases by name and reports pass/fail, latency, and token deltas.
func Compare(baseline, candidate []Result) Comparison {
	report := Comparison{Baseline: Summarize(baseline), Candidate: Summarize(candidate)}
	base := make(map[string]Result, len(baseline))
	cand := make(map[string]Result, len(candidate))
	for _, result := range baseline {
		base[result.Case.Name] = result
	}
	for _, result := range candidate {
		cand[result.Case.Name] = result
	}
	names := make([]string, 0, len(base)+len(cand))
	seen := map[string]bool{}
	for name := range base {
		names = append(names, name)
		seen[name] = true
	}
	for name := range cand {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		before, hasBefore := base[name]
		after, hasAfter := cand[name]
		row := CaseComparison{Name: name, BaselineStatus: resultStatus(before, hasBefore), CandidateStatus: resultStatus(after, hasAfter)}
		switch {
		case !hasBefore:
			report.Added++
		case !hasAfter:
			report.Missing++
			row.Regression = before.Passed && !before.Skipped
		case before.Passed && !before.Skipped && (!after.Passed || after.Skipped || after.Error != nil || after.ErrorText != ""):
			row.Regression = true
		case !before.Passed && !before.Skipped && after.Passed:
			row.Improvement = true
		}
		if hasBefore && hasAfter {
			row.LatencyDeltaMS = durationMS(after.Latency - before.Latency)
			row.TokenDelta = after.Tokens - before.Tokens
		}
		if row.Regression {
			report.Regressions++
		}
		if row.Improvement {
			report.Improvements++
		}
		report.Cases = append(report.Cases, row)
	}
	return report
}

func resultStatus(result Result, exists bool) string {
	if !exists {
		return "missing"
	}
	if result.Skipped {
		return "skipped"
	}
	if result.Error != nil || result.ErrorText != "" {
		return "error"
	}
	if result.Passed {
		return "passed"
	}
	return "failed"
}

func durationMS(value time.Duration) int64 { return value.Milliseconds() }

// PrintComparison renders the changes that matter to release decisions.
func PrintComparison(report Comparison, w io.Writer) {
	fmt.Fprintf(w, "\nBaseline comparison: %d regression(s), %d improvement(s), %d added, %d missing\n", report.Regressions, report.Improvements, report.Added, report.Missing)
	for _, row := range report.Cases {
		if !row.Regression && !row.Improvement && row.BaselineStatus == row.CandidateStatus && row.LatencyDeltaMS == 0 && row.TokenDelta == 0 {
			continue
		}
		marker := "·"
		if row.Regression {
			marker = "REGRESSION"
		} else if row.Improvement {
			marker = "IMPROVED"
		}
		fmt.Fprintf(w, "%-10s %-30s %s → %s · latency %dms · tokens %+d\n", marker, row.Name, row.BaselineStatus, row.CandidateStatus, row.LatencyDeltaMS, row.TokenDelta)
	}
}
