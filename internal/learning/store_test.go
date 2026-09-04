package learning

import (
	"path/filepath"
	"testing"
)

func TestStoreAddListAndDedupe(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID:   "agent-a",
		SessionID: "session-a",
		Kind:      "memory",
		Title:     "remember this",
		Content:   "Task: hello\n\nOutcome: world",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	dup, err := store.Add(Proposal{
		AgentID:   "agent-a",
		SessionID: "session-a",
		Kind:      "memory",
		Content:   "Task: hello\n\nOutcome: world",
	})
	if err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}
	if dup.ID != p.ID {
		t.Fatalf("duplicate ID = %q, want %q", dup.ID, p.ID)
	}
	proposals, err := store.List("agent-a", StatusPending, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(proposals) != 1 || proposals[0].Status != StatusPending {
		t.Fatalf("proposals = %#v, want one pending", proposals)
	}
}

func TestStoreUpdateStatusPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{AgentID: "agent-a", Content: "learned thing"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.UpdateStatus(p.ID, StatusAccepted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.List("agent-a", StatusAccepted, 10)
	if err != nil {
		t.Fatalf("List reopened: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("accepted proposals = %#v, want %s", got, p.ID)
	}
}

func TestStoreUpdateStatusMetaPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID: "agent-a",
		Kind:    "skill",
		Content: "skill draft",
		Meta:    map[string]string{"skill_name": "draft-skill"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	updated, err := store.UpdateStatusMeta(p.ID, StatusAccepted, map[string]string{
		"installed_path": "/tmp/draft-skill/SKILL.md",
	})
	if err != nil {
		t.Fatalf("UpdateStatusMeta: %v", err)
	}
	if updated.Meta["skill_name"] != "draft-skill" || updated.Meta["installed_path"] == "" {
		t.Fatalf("meta not merged: %#v", updated.Meta)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.List("agent-a", StatusAccepted, 10)
	if err != nil {
		t.Fatalf("List reopened: %v", err)
	}
	if len(got) != 1 || got[0].Meta["installed_path"] == "" {
		t.Fatalf("accepted proposals = %#v, want installed_path", got)
	}
}

func TestStoreUpdateDraftPersistsAndLocksReviewed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID: "agent-a",
		Title:   "original",
		Content: "old content",
		Meta:    map[string]string{"skill_name": "old-skill"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	updated, err := store.UpdateDraft(p.ID, "edited", "new content", map[string]string{"skill_name": "new-skill"})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.Title != "edited" || updated.Content != "new content" || updated.Meta["skill_name"] != "new-skill" {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := store.UpdateStatus(p.ID, StatusAccepted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if _, err := store.UpdateDraft(p.ID, "late edit", "should fail", nil); err == nil {
		t.Fatal("expected reviewed proposal edit to fail")
	}
}

func TestStoreSummaryCountsReviewHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mem, err := store.Add(Proposal{
		AgentID:    "agent-a",
		Kind:       "memory",
		Content:    "remember this",
		Confidence: 0.8,
		Source:     "post_run",
		Meta:       map[string]string{"tools_used": "search, fetch"},
	})
	if err != nil {
		t.Fatalf("Add memory: %v", err)
	}
	skill, err := store.Add(Proposal{
		AgentID:    "agent-a",
		Kind:       "skill",
		Content:    "skill draft",
		Confidence: 0.6,
		Source:     "manual_run_review",
		Meta:       map[string]string{"tools_used": "search"},
	})
	if err != nil {
		t.Fatalf("Add skill: %v", err)
	}
	if _, err := store.UpdateStatus(mem.ID, StatusRejected); err != nil {
		t.Fatalf("reject memory: %v", err)
	}
	if _, err := store.UpdateStatusMeta(skill.ID, StatusAccepted, map[string]string{"installed_path": "/skills/demo/SKILL.md"}); err != nil {
		t.Fatalf("accept skill: %v", err)
	}
	if _, err := store.Add(Proposal{AgentID: "agent-b", Kind: "procedure", Content: "other agent"}); err != nil {
		t.Fatalf("Add other: %v", err)
	}

	got, err := store.Summary("agent-a")
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Total != 2 || got.Accepted != 1 || got.Rejected != 1 || got.Pending != 0 {
		t.Fatalf("status counts = %+v", got)
	}
	if got.Memories != 1 || got.Skills != 1 || got.InstalledSkills != 1 || got.Procedures != 0 {
		t.Fatalf("kind counts = %+v", got)
	}
	if got.AverageConfidence < 0.69 || got.AverageConfidence > 0.71 {
		t.Fatalf("AverageConfidence = %v, want ~0.7", got.AverageConfidence)
	}
	if got.BySource["post_run"] != 1 || got.BySource["manual_run_review"] != 1 {
		t.Fatalf("BySource = %#v", got.BySource)
	}
	if got.ByTool["search"] != 2 || got.ByTool["fetch"] != 1 {
		t.Fatalf("ByTool = %#v", got.ByTool)
	}
	if got.LatestAt == nil {
		t.Fatal("LatestAt was not set")
	}
}
