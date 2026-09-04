package eval

import (
	"errors"
	"testing"
	"time"
)

func TestFilterSuiteUsesSuiteAndCaseTags(t *testing.T) {
	suite := Suite{
		Name: "sample",
		Tags: []string{"flagship"},
		Cases: []Case{
			{Name: "weather", Input: "weather", Tags: []string{"weather"}},
			{Name: "stock", Input: "stock", Tags: []string{"finance"}},
		},
	}

	got, err := FilterSuite(suite, []string{"weather"})
	if err != nil {
		t.Fatalf("FilterSuite case tag: %v", err)
	}
	if len(got.Cases) != 1 || got.Cases[0].Name != "weather" {
		t.Fatalf("expected weather case only, got %#v", got.Cases)
	}

	got, err = FilterSuite(suite, []string{"flagship"})
	if err != nil {
		t.Fatalf("FilterSuite suite tag: %v", err)
	}
	if len(got.Cases) != 2 {
		t.Fatalf("suite-level tag should include all cases, got %d", len(got.Cases))
	}
}

func TestFilterSuiteErrorsWhenNoTagsMatch(t *testing.T) {
	suite := Suite{
		Name:  "sample",
		Cases: []Case{{Name: "one", Input: "hello", Tags: []string{"smoke"}}},
	}
	if _, err := FilterSuite(suite, []string{"missing"}); err == nil {
		t.Fatal("expected an error for an unmatched tag filter")
	}
}

func TestSummarizeCountsAndLatency(t *testing.T) {
	results := []Result{
		{Passed: true, Latency: 10 * time.Millisecond, Tokens: 100},
		{Passed: true, Latency: 20 * time.Millisecond, Tokens: 200},
		{Passed: false, Latency: 30 * time.Millisecond, Tokens: 300},
		{Error: errors.New("boom"), Latency: 40 * time.Millisecond},
		{Skipped: true},
	}

	got := Summarize(results)
	if got.Total != 5 || got.Passed != 2 || got.Failed != 1 || got.Errors != 1 || got.Skipped != 1 {
		t.Fatalf("unexpected counts: %#v", got)
	}
	if got.TotalTokens != 600 || got.AverageToken != 200 {
		t.Fatalf("unexpected token summary: %#v", got)
	}
	if got.AvgLatencyMS != 25 || got.P50LatencyMS != 20 || got.P95LatencyMS != 40 {
		t.Fatalf("unexpected latency summary: %#v", got)
	}
}

func TestNormalizeTagsAcceptsCommaSeparatedValues(t *testing.T) {
	got := normalizeTags([]string{"weather, finance", "SMOKE"})
	for _, want := range []string{"weather", "finance", "smoke"} {
		if !got[want] {
			t.Fatalf("expected normalized tag %q in %#v", want, got)
		}
	}
}
