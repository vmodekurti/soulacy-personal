package learning

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func ev(t string, agent, sess string, at time.Time, payload any) message.Event {
	return message.Event{Type: t, AgentID: agent, SessionID: sess, Timestamp: at, Payload: payload}
}

func TestBuildEvidence_SkillReuseCountsAfterAcceptance(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	accepted := []Proposal{{
		ID:      "prop-1",
		AgentID: "a",
		Kind:    "skill",
		Status:  StatusAccepted,
		Meta:    map[string]string{"skill_name": "morning-brief"},
		// accepted one hour after base
		UpdatedAt: base.Add(time.Hour),
		CreatedAt: base,
	}}

	events := []message.Event{
		// read BEFORE acceptance -> must not count
		ev("tool.call", "a", "s0", base.Add(30*time.Minute), message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "morning-brief"}}),
		// two reads AFTER acceptance in two sessions -> 2 uses, 2 sessions
		ev("tool.call", "a", "s1", base.Add(2*time.Hour), message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "morning-brief"}}),
		ev("tool.call", "a", "s2", base.Add(3*time.Hour), message.ToolCall{Name: "read_skill_file", Arguments: map[string]any{"skill_name": "morning-brief"}}),
		// unrelated skill -> ignored
		ev("tool.call", "a", "s3", base.Add(4*time.Hour), message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "some-other"}}),
		// other agent -> ignored
		ev("tool.call", "b", "s4", base.Add(5*time.Hour), message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "morning-brief"}}),
	}

	got := BuildEvidence("a", events, accepted)
	if got.AcceptedSkills != 1 {
		t.Fatalf("AcceptedSkills = %d, want 1", got.AcceptedSkills)
	}
	if len(got.SkillReuse) != 1 {
		t.Fatalf("SkillReuse len = %d, want 1", len(got.SkillReuse))
	}
	sr := got.SkillReuse[0]
	if sr.SkillName != "morning-brief" {
		t.Fatalf("skill name = %q", sr.SkillName)
	}
	if sr.Uses != 2 {
		t.Fatalf("uses = %d, want 2 (pre-acceptance read excluded)", sr.Uses)
	}
	if sr.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", sr.Sessions)
	}
	if got.ReusedSkills != 1 || got.TotalSkillUses != 2 {
		t.Fatalf("reused=%d totalUses=%d, want 1 and 2", got.ReusedSkills, got.TotalSkillUses)
	}
	if sr.LastUsedAt == nil || !sr.LastUsedAt.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("last used = %v, want %v", sr.LastUsedAt, base.Add(3*time.Hour))
	}
}

func TestBuildEvidence_RepeatedErrorReduction(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ref := base.Add(time.Hour) // learning turned on here
	accepted := []Proposal{{
		ID: "p", AgentID: "a", Kind: "procedure", Status: StatusAccepted,
		UpdatedAt: ref, CreatedAt: base,
	}}

	// Same underlying failure with varying ids/numbers -> one signature.
	events := []message.Event{
		ev("error", "a", "s1", base.Add(10*time.Minute), map[string]any{"error": "timeout calling tool xyz after 3000ms (req 4f9a2b1c)"}),
		ev("error", "a", "s2", base.Add(20*time.Minute), map[string]any{"error": "timeout calling tool xyz after 5000ms (req 99aa88bb)"}),
		ev("error", "a", "s3", base.Add(30*time.Minute), map[string]any{"error": "timeout calling tool xyz after 1000ms (req dead1234)"}),
		// after learning: happens once
		ev("error", "a", "s4", base.Add(2*time.Hour), map[string]any{"error": "timeout calling tool xyz after 2000ms (req cafe5678)"}),
		// a tool.result error, distinct signature but only occurs once -> excluded (not repeated)
		ev("tool.result", "a", "s5", base.Add(3*time.Hour), message.ToolResult{Name: "fetch", Content: "connection refused", IsError: true}),
	}

	got := BuildEvidence("a", events, accepted)
	if got.ReferenceAt == nil || !got.ReferenceAt.Equal(ref) {
		t.Fatalf("reference = %v, want %v", got.ReferenceAt, ref)
	}
	if len(got.RepeatedErrors) != 1 {
		t.Fatalf("repeated errors = %d, want 1 (single-occurrence excluded)", len(got.RepeatedErrors))
	}
	tr := got.RepeatedErrors[0]
	if tr.Before != 3 || tr.After != 1 {
		t.Fatalf("before=%d after=%d, want 3 and 1", tr.Before, tr.After)
	}
	wantReduction := float64(3-1) / 3.0
	if tr.Reduction < wantReduction-1e-9 || tr.Reduction > wantReduction+1e-9 {
		t.Fatalf("reduction = %v, want %v", tr.Reduction, wantReduction)
	}
	if got.ErrorsBefore != 3 || got.ErrorsAfter != 1 {
		t.Fatalf("aggregate before=%d after=%d", got.ErrorsBefore, got.ErrorsAfter)
	}
}

func TestBuildEvidence_MapRoundTripPayloads(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	accepted := []Proposal{{
		ID: "p", AgentID: "a", Kind: "skill", Status: StatusAccepted,
		Meta: map[string]string{"skill_name": "brief"}, UpdatedAt: base, CreatedAt: base,
	}}
	// Simulate the JSONL round-trip form where payload is a map with "arguments".
	events := []message.Event{
		ev("tool.call", "a", "s1", base.Add(time.Hour), map[string]any{
			"name":      "read_skill",
			"arguments": map[string]any{"skill_name": "brief"},
		}),
	}
	got := BuildEvidence("a", events, accepted)
	if len(got.SkillReuse) != 1 || got.SkillReuse[0].Uses != 1 {
		t.Fatalf("map round-trip reuse not counted: %+v", got.SkillReuse)
	}
}

func TestBuildEvidence_FleetBreakdownAndTrend(t *testing.T) {
	base := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC) // Monday
	accepted := []Proposal{
		{
			ID: "pa", AgentID: "a", Kind: "skill", Status: StatusAccepted,
			Meta: map[string]string{"skill_name": "brief"}, UpdatedAt: base, CreatedAt: base,
		},
		{
			ID: "pb", AgentID: "b", Kind: "procedure", Status: StatusAccepted,
			UpdatedAt: base.AddDate(0, 0, 7), CreatedAt: base.AddDate(0, 0, 7),
		},
	}
	events := []message.Event{
		ev("tool.call", "a", "s1", base.Add(2*time.Hour), message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "brief"}}),
		ev("error", "a", "s0", base.Add(-24*time.Hour), map[string]any{"error": "provider test failed with 500"}),
		ev("error", "a", "s2", base.Add(3*time.Hour), map[string]any{"error": "provider test failed with 500"}),
		ev("message.out", "b", "s3", base.AddDate(0, 0, 7).Add(time.Hour), map[string]any{"text": "done"}),
	}

	got := BuildEvidence("", events, accepted)
	if len(got.Agents) != 2 {
		t.Fatalf("agents len = %d, want 2: %+v", len(got.Agents), got.Agents)
	}
	if got.Agents[0].AgentID != "a" || got.Agents[0].TotalSkillUses != 1 {
		t.Fatalf("first agent = %+v, want agent a with one skill use", got.Agents[0])
	}
	if len(got.Trend) != 8 {
		t.Fatalf("trend len = %d, want 8", len(got.Trend))
	}
	var acceptedCount, skillUses, runs int
	for _, b := range got.Trend {
		acceptedCount += b.Accepted
		skillUses += b.SkillUses
		runs += b.Runs
	}
	if acceptedCount != 2 || skillUses != 1 || runs != 4 {
		t.Fatalf("trend totals accepted=%d skillUses=%d runs=%d", acceptedCount, skillUses, runs)
	}
}

func TestErrorSignature_GroupsVariants(t *testing.T) {
	a := errorSignature(`Error: dial tcp 10.0.0.1:5432: connection refused`)
	b := errorSignature(`Error: dial tcp 192.168.1.9:5432: connection refused`)
	if a != b {
		t.Fatalf("signatures differ:\n a=%q\n b=%q", a, b)
	}
	if errorSignature("   ") != "" {
		t.Fatalf("blank error should yield empty signature")
	}
}
