package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Suite is a collection of test cases for an agent.
type Suite struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description,omitempty"`
	Defaults    CaseDefaults   `yaml:"defaults" json:"defaults,omitempty"`
	Cases       []Case         `yaml:"cases" json:"cases"`
	Tags        []string       `yaml:"tags" json:"tags,omitempty"`
	Metadata    map[string]any `yaml:"metadata" json:"metadata,omitempty"`
}

// CaseDefaults contains suite-level defaults applied to each case.
type CaseDefaults struct {
	UserID     string         `yaml:"user_id" json:"user_id,omitempty"`
	TimeoutSec int            `yaml:"timeout_sec" json:"timeout_sec,omitempty"`
	Overrides  map[string]any `yaml:"overrides" json:"overrides,omitempty"`
}

// RunOptions controls how a suite is executed.
type RunOptions struct {
	// Tags limits execution to cases that carry at least one of these tags, or
	// to all cases in a suite that carries a matching suite-level tag.
	Tags []string
	// Repeat runs the selected cases multiple times to expose flaky behavior and
	// latency variance. Values <= 0 are treated as 1.
	Repeat int
	// FailFast stops after the first failed/error case.
	FailFast bool
}

// Case is one evaluation test case.
type Case struct {
	Name                string         `yaml:"name" json:"name"`
	Input               string         `yaml:"input" json:"input"`
	UserID              string         `yaml:"user_id" json:"user_id,omitempty"`
	SessionID           string         `yaml:"session_id" json:"session_id,omitempty"`
	ExpectedContains    []string       `yaml:"expected_contains" json:"expected_contains"`             // all must appear in reply (case-insensitive)
	ExpectedNotContains []string       `yaml:"expected_not_contains" json:"expected_not_contains"`     // none must appear
	ExpectedRegex       []string       `yaml:"expected_regex" json:"expected_regex,omitempty"`         // all regexes must match reply
	ExpectedNotRegex    []string       `yaml:"expected_not_regex" json:"expected_not_regex,omitempty"` // no regex may match reply
	MaxTokens           int            `yaml:"max_tokens" json:"max_tokens"`                           // 0 = no limit check
	MaxLatencyMS        int            `yaml:"max_latency_ms" json:"max_latency_ms,omitempty"`         // 0 = no limit check
	TimeoutSec          int            `yaml:"timeout_sec" json:"timeout_sec"`                         // default 30
	Overrides           map[string]any `yaml:"overrides" json:"overrides,omitempty"`                   // chat override payload
	Tags                []string       `yaml:"tags" json:"tags,omitempty"`

	// ExpectToolSuccess lists tool names that must appear in the reply's tool
	// trace and must have succeeded. Requires the gateway to return a "tools"
	// array of {name, ok} objects; asserted only when this list is non-empty.
	ExpectToolSuccess []string `yaml:"expect_tool_success" json:"expect_tool_success,omitempty"`
	// ExpectDelivered, when set, asserts the response's channel-delivery flag.
	// nil = no assertion; requires the gateway to return a "delivered" boolean.
	ExpectDelivered *bool `yaml:"expect_delivered" json:"expect_delivered,omitempty"`
	// RequiresSecret lists environment variable names that must be non-empty
	// for this case to run. If any is unset the case is SKIPPED (not failed)
	// with a clear reason — this is how secret-backed cases self-document why
	// they don't run in CI without leaking the secret itself.
	RequiresSecret []string `yaml:"requires_secret" json:"requires_secret,omitempty"`
}

// Result is the outcome of running one Case.
type Result struct {
	Case       Case          `json:"case"`
	Passed     bool          `json:"passed"`
	Skipped    bool          `json:"skipped,omitempty"` // requirements (e.g. secrets) not met
	SkipReason string        `json:"skip_reason,omitempty"`
	Reply      string        `json:"reply,omitempty"`
	Latency    time.Duration `json:"latency"`
	Tokens     int           `json:"tokens,omitempty"`
	Error      error         `json:"-"`
	ErrorText  string        `json:"error,omitempty"`
	Reasons    []string      `json:"reasons,omitempty"` // why it failed
}

// Summary is the aggregate outcome of one eval run.
type Summary struct {
	Total        int `json:"total"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Errors       int `json:"errors"`
	Skipped      int `json:"skipped"`
	TotalTokens  int `json:"total_tokens"`
	AverageToken int `json:"average_tokens"`
	AvgLatencyMS int `json:"avg_latency_ms"`
	P50LatencyMS int `json:"p50_latency_ms"`
	P95LatencyMS int `json:"p95_latency_ms"`
}

// Runner executes a Suite against a live gateway.
type Runner struct {
	GatewayURL string
	APIKey     string
	AgentID    string
	UserID     string // default "eval-runner"
}

// NewRunner creates a new Runner with the given gateway URL, API key, and agent ID.
func NewRunner(gatewayURL, apiKey, agentID string) *Runner {
	return &Runner{
		GatewayURL: gatewayURL,
		APIKey:     apiKey,
		AgentID:    agentID,
		UserID:     "eval-runner",
	}
}

// Run executes all cases in a suite sequentially and returns results.
func (r *Runner) Run(ctx context.Context, suite Suite) ([]Result, error) {
	return r.RunWithOptions(ctx, suite, RunOptions{})
}

// RunWithOptions executes selected cases in a suite and returns results.
func (r *Runner) RunWithOptions(ctx context.Context, suite Suite, opts RunOptions) ([]Result, error) {
	suite, err := FilterSuite(suite, opts.Tags)
	if err != nil {
		return nil, err
	}
	repeat := opts.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	results := make([]Result, 0, len(suite.Cases))
	for rep := 0; rep < repeat; rep++ {
		for idx, c := range suite.Cases {
			c = applyDefaults(c, suite.Defaults)
			if repeat > 1 {
				c.Name = fmt.Sprintf("%s [%d/%d]", c.Name, rep+1, repeat)
			}
			result := r.runCase(ctx, rep*len(suite.Cases)+idx, c)
			results = append(results, result)
			if opts.FailFast && !result.Skipped && (!result.Passed || result.Error != nil) {
				return results, nil
			}
		}
	}
	return results, nil
}

// runCase executes a single test case and returns its result.
func (r *Runner) runCase(ctx context.Context, idx int, c Case) Result {
	// Skip secret-backed cases when their required environment variables are
	// missing, with an explicit (non-secret-leaking) reason. This lets CI run
	// the non-secret subset and lets local runs document exactly what to set.
	if missing := missingSecrets(c.RequiresSecret); len(missing) > 0 {
		return Result{
			Case:       c,
			Skipped:    true,
			SkipReason: "missing required secret(s): " + strings.Join(missing, ", "),
		}
	}

	timeoutSec := c.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	caseCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	userID := r.UserID
	if userID == "" {
		userID = "eval-runner"
	}
	if strings.TrimSpace(c.UserID) != "" {
		userID = strings.TrimSpace(c.UserID)
	}

	sessionID := strings.TrimSpace(c.SessionID)
	if sessionID == "" {
		sessionID = fmt.Sprintf("eval-%d-%02d-%s", time.Now().UnixNano(), idx+1, slug(c.Name))
	}
	payload := map[string]any{
		"agent_id":   r.AgentID,
		"user_id":    userID,
		"session_id": sessionID,
		"text":       c.Input,
	}
	if len(c.Overrides) > 0 {
		payload["overrides"] = c.Overrides
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return resultError(c, 0, fmt.Errorf("failed to marshal request: %w", err))
	}

	req, err := http.NewRequestWithContext(caseCtx, http.MethodPost, r.GatewayURL+"/api/v1/chat", bytes.NewReader(body))
	if err != nil {
		return resultError(c, 0, fmt.Errorf("failed to create request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	start := time.Now()
	client := &http.Client{}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return resultError(c, latency, fmt.Errorf("request failed: %w", err))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resultError(c, latency, fmt.Errorf("failed to read response: %w", err))
	}

	if resp.StatusCode >= 400 {
		return resultError(c, latency, fmt.Errorf("gateway error %d: %s", resp.StatusCode, string(data)))
	}

	var parsed struct {
		Reply  string `json:"reply"`
		Tokens int    `json:"tokens"`
		Tools  []struct {
			Name  string `json:"name"`
			OK    *bool  `json:"ok"`
			Error string `json:"error"`
		} `json:"tools"`
		Delivered *bool `json:"delivered"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return resultError(c, latency, fmt.Errorf("failed to parse response: %w", err))
	}

	var reasons []string
	replyLower := strings.ToLower(parsed.Reply)

	for _, expected := range c.ExpectedContains {
		if !strings.Contains(replyLower, strings.ToLower(expected)) {
			reasons = append(reasons, fmt.Sprintf("missing expected: %q", expected))
		}
	}

	for _, forbidden := range c.ExpectedNotContains {
		if strings.Contains(replyLower, strings.ToLower(forbidden)) {
			reasons = append(reasons, fmt.Sprintf("found forbidden: %q", forbidden))
		}
	}

	for _, pattern := range c.ExpectedRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("invalid expected regex %q: %v", pattern, err))
			continue
		}
		if !re.MatchString(parsed.Reply) {
			reasons = append(reasons, fmt.Sprintf("missing regex match: %q", pattern))
		}
	}

	for _, pattern := range c.ExpectedNotRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("invalid forbidden regex %q: %v", pattern, err))
			continue
		}
		if re.MatchString(parsed.Reply) {
			reasons = append(reasons, fmt.Sprintf("matched forbidden regex: %q", pattern))
		}
	}

	// Tool-success assertions: each named tool must have run and succeeded.
	for _, want := range c.ExpectToolSuccess {
		found, ok := false, false
		var toolErr string
		for _, t := range parsed.Tools {
			if strings.EqualFold(t.Name, want) {
				found = true
				ok = t.OK == nil || *t.OK // absent ok defaults to success
				toolErr = t.Error
				break
			}
		}
		switch {
		case !found:
			reasons = append(reasons, fmt.Sprintf("expected tool not called: %q", want))
		case !ok:
			reasons = append(reasons, fmt.Sprintf("tool %q failed: %s", want, strings.TrimSpace(toolErr)))
		}
	}

	// Channel-delivery assertion: reply must report the expected delivery state.
	if c.ExpectDelivered != nil {
		if parsed.Delivered == nil {
			reasons = append(reasons, "expected channel-delivery status but response reported none")
		} else if *parsed.Delivered != *c.ExpectDelivered {
			reasons = append(reasons, fmt.Sprintf("delivery mismatch: got delivered=%v, want %v", *parsed.Delivered, *c.ExpectDelivered))
		}
	}

	if c.MaxTokens > 0 && parsed.Tokens > c.MaxTokens {
		reasons = append(reasons, fmt.Sprintf("token limit exceeded: %d > %d", parsed.Tokens, c.MaxTokens))
	}
	if c.MaxLatencyMS > 0 && latency > time.Duration(c.MaxLatencyMS)*time.Millisecond {
		reasons = append(reasons, fmt.Sprintf("latency limit exceeded: %s > %dms", latency.Round(time.Millisecond), c.MaxLatencyMS))
	}

	return Result{
		Case:    c,
		Passed:  len(reasons) == 0,
		Reply:   parsed.Reply,
		Latency: latency,
		Tokens:  parsed.Tokens,
		Reasons: reasons,
	}
}

// PrintReport writes a formatted report of eval results to w.
func PrintReport(results []Result, w io.Writer) {
	summary := Summarize(results)

	// Header
	fmt.Fprintf(w, "%-30s  %-6s  %-10s  %-6s  %s\n", "CASE", "RESULT", "LATENCY", "TOKENS", "REASON")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 80))

	for _, r := range results {
		status := "PASS"
		reason := ""
		if r.Skipped {
			status = "SKIP"
			reason = r.SkipReason
		} else if r.Error != nil {
			status = "ERROR"
			reason = r.Error.Error()
		} else if !r.Passed {
			status = "FAIL"
			reason = strings.Join(r.Reasons, "; ")
		}

		latency := "-"
		if r.Latency > 0 {
			latency = r.Latency.Round(time.Millisecond).String()
		}

		tokens := "-"
		if r.Tokens > 0 {
			tokens = fmt.Sprintf("%d", r.Tokens)
		}

		name := r.Case.Name
		if len(name) > 29 {
			name = name[:26] + "..."
		}
		if len(reason) > 40 {
			reason = reason[:37] + "..."
		}

		fmt.Fprintf(w, "%-30s  %-6s  %-10s  %-6s  %s\n", name, status, latency, tokens, reason)
	}

	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 80))
	if summary.Skipped > 0 {
		fmt.Fprintf(w, "%d/%d passed, %d skipped", summary.Passed, summary.Total-summary.Skipped, summary.Skipped)
	} else {
		fmt.Fprintf(w, "%d/%d passed", summary.Passed, summary.Total)
	}
	if summary.Failed > 0 || summary.Errors > 0 {
		fmt.Fprintf(w, " (%d failed, %d errors)", summary.Failed, summary.Errors)
	}
	if summary.AvgLatencyMS > 0 {
		fmt.Fprintf(w, " · latency avg/p50/p95 %d/%d/%dms", summary.AvgLatencyMS, summary.P50LatencyMS, summary.P95LatencyMS)
	}
	if summary.TotalTokens > 0 {
		fmt.Fprintf(w, " · tokens total/avg %d/%d", summary.TotalTokens, summary.AverageToken)
	}
	fmt.Fprintln(w)
}

// Summarize aggregates result counts, token use, and latency percentiles.
func Summarize(results []Result) Summary {
	var s Summary
	s.Total = len(results)
	var latencies []int
	tokenCases := 0
	for _, r := range results {
		if r.Skipped {
			s.Skipped++
			continue
		}
		if r.Error != nil {
			s.Errors++
		} else if r.Passed {
			s.Passed++
		} else {
			s.Failed++
		}
		if r.Tokens > 0 {
			s.TotalTokens += r.Tokens
			tokenCases++
		}
		if r.Latency > 0 {
			latencies = append(latencies, int(r.Latency.Round(time.Millisecond)/time.Millisecond))
		}
	}
	if tokenCases > 0 {
		s.AverageToken = s.TotalTokens / tokenCases
	}
	if len(latencies) > 0 {
		sort.Ints(latencies)
		sum := 0
		for _, v := range latencies {
			sum += v
		}
		s.AvgLatencyMS = sum / len(latencies)
		s.P50LatencyMS = percentile(latencies, 50)
		s.P95LatencyMS = percentile(latencies, 95)
	}
	return s
}

// FilterSuite returns a copy of suite narrowed to matching case tags. Suite tags
// match all cases, making it easy to run a whole category such as "weather".
func FilterSuite(suite Suite, tags []string) (Suite, error) {
	need := normalizeTags(tags)
	if len(need) == 0 {
		return suite, nil
	}
	if tagsIntersect(suite.Tags, need) {
		return suite, nil
	}
	filtered := suite
	filtered.Cases = nil
	for _, c := range suite.Cases {
		if tagsIntersect(c.Tags, need) {
			filtered.Cases = append(filtered.Cases, c)
		}
	}
	if len(filtered.Cases) == 0 {
		return Suite{}, fmt.Errorf("no eval cases matched tag filter: %s", strings.Join(tags, ", "))
	}
	return filtered, nil
}

// LoadSuite parses a Suite from JSON or YAML bytes.
func LoadSuite(data []byte) (Suite, error) {
	if suite, err := LoadSuiteFromJSON(data); err == nil {
		return suite, nil
	}
	var suite Suite
	if err := yaml.Unmarshal(data, &suite); err != nil {
		return Suite{}, fmt.Errorf("failed to parse eval suite as JSON or YAML: %w", err)
	}
	if err := validateSuite(suite); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

// LoadSuiteFromJSON parses a Suite from JSON bytes.
func LoadSuiteFromJSON(data []byte) (Suite, error) {
	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, fmt.Errorf("failed to parse eval suite: %w", err)
	}
	if err := validateSuite(suite); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func validateSuite(suite Suite) error {
	if len(suite.Cases) == 0 {
		return fmt.Errorf("eval suite must contain at least one case")
	}
	for i, c := range suite.Cases {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("case %d is missing name", i+1)
		}
		if strings.TrimSpace(c.Input) == "" {
			return fmt.Errorf("case %q is missing input", c.Name)
		}
	}
	return nil
}

func applyDefaults(c Case, d CaseDefaults) Case {
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = d.TimeoutSec
	}
	if strings.TrimSpace(c.UserID) == "" {
		c.UserID = strings.TrimSpace(d.UserID)
	}
	if len(d.Overrides) > 0 {
		merged := map[string]any{}
		keys := make([]string, 0, len(d.Overrides))
		for k := range d.Overrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			merged[k] = d.Overrides[k]
		}
		for k, v := range c.Overrides {
			merged[k] = v
		}
		c.Overrides = merged
	}
	return c
}

func resultError(c Case, latency time.Duration, err error) Result {
	return Result{Case: c, Latency: latency, Error: err, ErrorText: err.Error(), Reasons: []string{err.Error()}}
}

// missingSecrets returns the subset of the given environment variable names
// that are unset or empty.
func missingSecrets(names []string) []string {
	var missing []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if strings.TrimSpace(os.Getenv(n)) == "" {
			missing = append(missing, n)
		}
	}
	return missing
}

func normalizeTags(tags []string) map[string]bool {
	out := map[string]bool{}
	for _, tag := range tags {
		for _, part := range strings.Split(tag, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				out[part] = true
			}
		}
	}
	return out
}

func tagsIntersect(tags []string, need map[string]bool) bool {
	for _, tag := range tags {
		if need[strings.ToLower(strings.TrimSpace(tag))] {
			return true
		}
	}
	return false
}

func percentile(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := (p*len(sorted) + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "case"
	}
	if len(out) > 40 {
		return out[:40]
	}
	return out
}
