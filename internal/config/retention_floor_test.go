// retention_floor_test.go — MU-031 criterion 5: "retention is configurable
// within platform minimums."
//
// The failure this bounds is not storage cost. Retention here is how long a
// deployment can answer "who changed this", and both ways of losing that were
// reachable through a route that is itself audited — so the change was
// recorded and its effect was not.
package config

import (
	"strings"
	"testing"
)

func retentionConfig(t *testing.T, actionEvents, auditLogs, history string) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.Runtime.Retention.ActionEvents = actionEvents
	cfg.Runtime.Retention.AuditLogs = auditLogs
	cfg.Runtime.Retention.ConversationHistory = history
	return cfg
}

// retentionErrors keeps only the retention complaints. A bare Config fails
// validation for many unrelated reasons, and asserting on "did Validate fail"
// would pass whatever the retention rules did — the shape of test that
// succeeds for the wrong reason.
func retentionErrors(t *testing.T, cfg *Config) []string {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "runtime.retention") {
			out = append(out, line)
		}
	}
	return out
}

// A NEGATIVE duration parses fine, and every downstream guard is
// `retention > 0` — so `-1h` silently turned pruning OFF rather than
// shortening it, and an operator reading the config saw a policy that was not
// in force.
func TestANegativeRetentionIsRefusedRatherThanSilentlyDisablingPruning(t *testing.T) {
	errs := retentionErrors(t, retentionConfig(t, "-1h", "", ""))
	if len(errs) == 0 {
		t.Fatal("a negative action-events retention was accepted")
	}
	if !strings.Contains(strings.Join(errs, " "), "negative") {
		t.Fatalf("the error does not explain what a negative retention does: %v", errs)
	}
	// Conversation history is user content rather than an audit record, so it
	// gets the negative check too — a policy that silently does not run is the
	// same bug whatever it governs.
	if len(retentionErrors(t, retentionConfig(t, "", "", "-5m"))) == 0 {
		t.Fatal("a negative conversation-history retention was accepted")
	}
}

func TestRetentionBelowThePlatformMinimumIsRefused(t *testing.T) {
	for _, short := range []string{"1m", "24h", "168h"} {
		if len(retentionErrors(t, retentionConfig(t, short, "", ""))) == 0 {
			t.Errorf("action_events retention of %s was accepted below the %s minimum", short, MinAuditRetention)
		}
		if len(retentionErrors(t, retentionConfig(t, "", short, ""))) == 0 {
			t.Errorf("audit_logs retention of %s was accepted below the %s minimum", short, MinAuditRetention)
		}
	}
}

// The floor bounds how SHORT retention may be. "0" means keep forever, and
// forever is not short — refusing it would force every deployment onto a
// deletion schedule it did not ask for.
func TestForeverAndTheDefaultsAreAccepted(t *testing.T) {
	if errs := retentionErrors(t, retentionConfig(t, "0", "0", "0")); len(errs) != 0 {
		t.Fatalf("keep-forever was refused: %v", errs)
	}
	if errs := retentionErrors(t, retentionConfig(t, "", "", "")); len(errs) != 0 {
		t.Fatalf("an unset retention was refused: %v", errs)
	}
	// And the values configs/default.yaml ships must satisfy their own floor,
	// or every install starts invalid. Stated literally rather than loaded,
	// because the point is that these exact numbers are above the line.
	if errs := retentionErrors(t, retentionConfig(t, "2160h", "720h", "720h")); len(errs) != 0 {
		t.Fatalf("the shipped retention defaults fail their own floor: %v", errs)
	}
}

// Conversation history is a privacy choice, not an evasion of one, so it has
// no floor.
func TestConversationHistoryHasNoFloor(t *testing.T) {
	if errs := retentionErrors(t, retentionConfig(t, "", "", "24h")); len(errs) != 0 {
		t.Fatalf("a short conversation-history retention was refused: %v", errs)
	}
}
