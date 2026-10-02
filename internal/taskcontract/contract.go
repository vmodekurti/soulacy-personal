// Package taskcontract records the runtime's evidence-based view of one agent run.
// It is deliberately deterministic: models may propose plans and describe results,
// but only runtime-observed inputs, tool results, and outcome assertions change the
// contract's state.
package taskcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/trust"
	"github.com/soulacy/soulacy/pkg/message"
)

const (
	Version             = "1"
	maxSummaryRunes     = 512
	maxEvidenceSummary  = 320
	maxEvidenceRecords  = 32
	defaultMaxReplans   = 2
	OutcomeDirectAnswer = "direct_answer"
	OutcomeEvidence     = "evidence_based"
	OutcomeAttempted    = "attempted"
	OutcomeNeedsInput   = "needs_input"
	OutcomeVerified     = "verified_action"
	OutcomeBlocked      = "blocked"
	OutcomeFailed       = "failed"
)

// Perception is the normalized, bounded record of what entered the run.
type Perception struct {
	Source             string    `json:"source"`
	Trigger            string    `json:"trigger,omitempty"`
	Actor              string    `json:"actor,omitempty"`
	ReceivedAt         time.Time `json:"received_at"`
	FreshnessSeconds   int64     `json:"freshness_seconds,omitempty"`
	DataClassification string    `json:"data_classification,omitempty"`
	Trust              string    `json:"trust"`
	Confidence         float64   `json:"confidence"`
	ContentBytes       int       `json:"content_bytes"`
	ContentSHA256      string    `json:"content_sha256"`
	Summary            string    `json:"summary"`
}

// Budget is the runtime-enforced allowance for the run.
type Budget struct {
	MaxTurns    int `json:"max_turns,omitempty"`
	MaxTokens   int `json:"max_tokens,omitempty"`
	MaxLLMCalls int `json:"max_llm_calls,omitempty"`
	MaxReplans  int `json:"max_replans"`
}

// Evidence records one bounded, redacted observation. Content is represented by
// a digest and short summary so task telemetry cannot become a second secret log.
type Evidence struct {
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Success    bool   `json:"success"`
	Verified   bool   `json:"verified,omitempty"`
	Trust      string `json:"trust"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	Summary    string `json:"summary,omitempty"`
	ObservedAt string `json:"observed_at"`
}

// Snapshot is the serializable public record. It contains no chain of thought.
type Snapshot struct {
	Version            string     `json:"version"`
	RunID              string     `json:"run_id"`
	AgentID            string     `json:"agent_id"`
	SessionID          string     `json:"session_id"`
	Goal               string     `json:"goal"`
	Mode               string     `json:"mode"`
	State              string     `json:"state"`
	Outcome            string     `json:"outcome,omitempty"`
	Strategy           string     `json:"strategy,omitempty"`
	CompletionCriteria []string   `json:"completion_criteria"`
	FallbackRoutes     []string   `json:"fallback_routes"`
	Budget             Budget     `json:"budget"`
	Perception         Perception `json:"perception"`
	Attempts           int        `json:"attempts"`
	Replans            int        `json:"replans"`
	Evidence           []Evidence `json:"evidence,omitempty"`
	Blocker            string     `json:"blocker,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

// Contract is a concurrency-safe reducer for tool calls that may execute in parallel.
type Contract struct {
	mu               sync.Mutex
	snapshot         Snapshot
	failedRoutes     map[string]bool
	pendingReplan    bool
	replanDispatched int
	verified         bool
	toolSuccesses    int
	toolFailures     int
	awaitingInput    bool
}

func New(runID string, msg message.Message, now time.Time) *Contract {
	goal := flatten(msg.Parts)
	created := msg.CreatedAt
	if created.IsZero() || created.After(now) {
		created = now
	}
	actor := strings.TrimSpace(msg.UserID)
	if actor == "" {
		actor = strings.TrimSpace(msg.Username)
	}
	classification := "unspecified"
	trigger := ""
	if msg.Metadata != nil {
		if v := strings.TrimSpace(msg.Metadata["data_classification"]); v != "" {
			classification = v
		}
		trigger = strings.TrimSpace(msg.Metadata["trigger"])
	}
	trust := "user_supplied"
	if msg.Channel == "internal" {
		trust = "trusted_internal"
	}
	return &Contract{
		snapshot: Snapshot{
			Version: Version, RunID: runID, AgentID: msg.AgentID, SessionID: msg.SessionID,
			Goal: bounded(redact.Text(goal), maxSummaryRunes), Mode: taskMode(goal), State: "received",
			CompletionCriteria: []string{
				"answer the stated goal",
				"ground claims in runtime-observed evidence",
				"verify an external effect before reporting it as completed",
			},
			FallbackRoutes: []string{
				"available connector or native API",
				"authorized website or browser action",
				"official alternative route",
				"request only the missing human input",
			},
			Budget: Budget{MaxReplans: defaultMaxReplans},
			Perception: Perception{
				Source: strings.TrimSpace(msg.Channel), Trigger: trigger,
				Actor: bounded(redact.Text(actor), 128), ReceivedAt: created,
				FreshnessSeconds:   int64(now.Sub(created).Seconds()),
				DataClassification: classification, Trust: trust, Confidence: 1,
				ContentBytes: len([]byte(goal)), ContentSHA256: digest(goal),
				Summary: bounded(redact.Text(goal), maxSummaryRunes),
			},
			StartedAt: now,
		},
		failedRoutes: make(map[string]bool),
	}
}

func (c *Contract) Configure(strategy string, maxTurns, maxTokens, maxCalls int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.Strategy = strings.TrimSpace(strategy)
	c.snapshot.Budget.MaxTurns = maxTurns
	c.snapshot.Budget.MaxTokens = maxTokens
	c.snapshot.Budget.MaxLLMCalls = maxCalls
	if c.snapshot.State == "received" {
		c.snapshot.State = "planning"
	}
}

// ObserveTool adds runtime evidence and schedules a bounded replan after a
// failed route. A model's prose never calls this method.
func (c *Contract) ObserveTool(call message.ToolCall, result string, failed bool) {
	if c == nil {
		return
	}
	name := strings.TrimSpace(call.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.Attempts++
	verified := !failed && verificationEvidence(name, result)
	trustLevel := trust.ToolTrust(name).String()
	if failed {
		trustLevel = trust.Trusted.String()
	}
	e := Evidence{
		Kind: "tool_result", Source: name, Success: !failed, Verified: verified,
		Trust: trustLevel,
		Bytes: len([]byte(result)), SHA256: digest(result),
		Summary:    bounded(redact.Text(firstLine(result)), maxEvidenceSummary),
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if len(c.snapshot.Evidence) < maxEvidenceRecords {
		c.snapshot.Evidence = append(c.snapshot.Evidence, e)
	}
	if failed {
		c.toolFailures++
		if name == "" {
			name = "unknown"
		}
		if !c.failedRoutes[name] && c.snapshot.Replans < c.snapshot.Budget.MaxReplans {
			c.failedRoutes[name] = true
			c.snapshot.Replans++
			c.pendingReplan = true
			c.snapshot.State = "replanning"
		}
		return
	}
	c.toolSuccesses++
	if resultStatus(result) == OutcomeNeedsInput {
		c.awaitingInput = true
	}
	c.snapshot.State = "executing"
	if verified {
		c.verified = true
	}
}

// ReplanDirective returns at most one instruction per newly failed route and
// consumes it. This lets the runtime steer the next model turn without loops.
func (c *Contract) ReplanDirective() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pendingReplan || c.replanDispatched >= c.snapshot.Budget.MaxReplans {
		return ""
	}
	c.pendingReplan = false
	c.replanDispatched++
	return "The selected route failed. Replan at the capability level and do not repeat that route. Try, in order: an available connector or native API; an authorized website or browser action; an official alternative route; then request only the missing human input. If none can work, state the blocker clearly and stop."
}

func (c *Contract) MarkVerified(source, summary string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.verified = true
	c.snapshot.Evidence = appendBounded(c.snapshot.Evidence, Evidence{
		Kind: "outcome_assertion", Source: source, Success: true, Verified: true,
		Trust: trust.Trusted.String(),
		Bytes: len(summary), SHA256: digest(summary),
		Summary:    bounded(redact.Text(summary), maxEvidenceSummary),
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (c *Contract) MarkBlocked(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.State = "blocked"
	c.snapshot.Outcome = OutcomeBlocked
	c.snapshot.Blocker = bounded(redact.Text(reason), maxSummaryRunes)
}

// Complete derives the terminal class from evidence observed by the runtime.
func (c *Contract) Complete(runErr error, successful, degraded bool, now time.Time) Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if runErr != nil || !successful {
		c.snapshot.State = "failed"
		c.snapshot.Outcome = OutcomeFailed
		if runErr != nil {
			c.snapshot.Blocker = bounded(redact.Text(runErr.Error()), maxSummaryRunes)
		}
	} else if c.snapshot.Outcome == OutcomeBlocked {
		c.snapshot.State = "blocked"
	} else {
		c.snapshot.State = "completed"
		switch {
		case degraded:
			c.snapshot.Outcome = OutcomeBlocked
			if c.snapshot.Blocker == "" {
				c.snapshot.Blocker = "completion criteria were not met"
			}
		case c.verified:
			c.snapshot.Outcome = OutcomeVerified
		case c.toolFailures > 0 && c.toolSuccesses == 0:
			c.snapshot.State = "blocked"
			c.snapshot.Outcome = OutcomeBlocked
			if c.snapshot.Blocker == "" {
				c.snapshot.Blocker = "tool routes failed without successful evidence"
			}
		case c.snapshot.Mode == "external_action" && c.awaitingInput:
			c.snapshot.State = "waiting_for_input"
			c.snapshot.Outcome = OutcomeNeedsInput
		case c.snapshot.Mode == "external_action" && c.snapshot.Attempts > 0:
			c.snapshot.State = "incomplete"
			c.snapshot.Outcome = OutcomeAttempted
		case c.snapshot.Attempts > 0 || c.toolSuccesses > 0:
			c.snapshot.Outcome = OutcomeEvidence
		case c.snapshot.Mode == "external_action":
			c.snapshot.State = "blocked"
			c.snapshot.Outcome = OutcomeBlocked
			c.snapshot.Blocker = "the requested external action has no runtime evidence"
		default:
			c.snapshot.Outcome = OutcomeDirectAnswer
		}
	}
	t := now
	c.snapshot.CompletedAt = &t
	return clone(c.snapshot)
}

func (c *Contract) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return clone(c.snapshot)
}

func verificationEvidence(name, result string) bool {
	var payload map[string]any
	if trust.ToolTrust(name) == trust.Trusted && json.Unmarshal([]byte(result), &payload) == nil {
		if v, ok := payload["verified"].(bool); ok && v {
			return true
		}
		status := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["status"])))
		switch status {
		case "confirmed", "committed", "booked", "delivered":
			return true
		}
	}
	return false
}

func resultStatus(result string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(result), &payload) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["status"])))
}

func taskMode(goal string) string {
	g := strings.ToLower(strings.TrimSpace(goal))
	if g == "" {
		return "direct"
	}
	for _, prefix := range []string{
		"please ", "can you ", "could you ", "would you ", "i want you to ",
		"i need you to ", "i want to ", "have genie ", "tell genie to ",
	} {
		if strings.HasPrefix(g, prefix) {
			g = strings.TrimSpace(strings.TrimPrefix(g, prefix))
			break
		}
	}
	for _, prefix := range []string{
		"book ", "reserve ", "buy ", "purchase ", "order ", "pay ", "transfer ",
		"deploy ", "install ", "restart ", "upload ", "publish ", "cancel ",
		"delete ", "remove ", "send an email", "send a message", "schedule ",
		"sign in ", "log in ",
	} {
		if strings.HasPrefix(g, prefix) {
			return "external_action"
		}
	}
	return "direct"
}

func appendBounded(in []Evidence, e Evidence) []Evidence {
	if len(in) >= maxEvidenceRecords {
		return in
	}
	return append(in, e)
}

func clone(in Snapshot) Snapshot {
	out := in
	out.CompletionCriteria = append([]string(nil), in.CompletionCriteria...)
	out.FallbackRoutes = append([]string(nil), in.FallbackRoutes...)
	out.Evidence = append([]Evidence(nil), in.Evidence...)
	return out
}

func flatten(parts []message.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == message.ContentText && strings.TrimSpace(p.Text) != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(strings.TrimSpace(p.Text))
		}
	}
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func bounded(s string, limit int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return strings.TrimSpace(string(r[:limit])) + "..."
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
