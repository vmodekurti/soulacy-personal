package eval

import (
	"testing"
	"time"
)

func TestCompareFindsRegressionsAndImprovements(t *testing.T) {
	baseline := []Result{
		{Case: Case{Name: "stable"}, Passed: true, Latency: time.Second, Tokens: 10},
		{Case: Case{Name: "fixed"}, Passed: false},
	}
	candidate := []Result{
		{Case: Case{Name: "stable"}, Passed: false, Latency: 2 * time.Second, Tokens: 15},
		{Case: Case{Name: "fixed"}, Passed: true},
	}
	report := Compare(baseline, candidate)
	if report.Regressions != 1 || report.Improvements != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Cases[1].LatencyDeltaMS != 1000 || report.Cases[1].TokenDelta != 5 {
		t.Fatalf("stable delta = %+v", report.Cases[1])
	}
}
