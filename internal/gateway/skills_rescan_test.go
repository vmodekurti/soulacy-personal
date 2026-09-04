package gateway

// Story E18: POST /api/v1/skills/rescan hot-loads freshly installed skills.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/skill"
)

// rescanSkillLoader satisfies runtime.SkillLoader plus the Scan() extension
// the handler type-asserts on.
type rescanSkillLoader struct {
	scanned int
	skills  []*skill.Skill
}

func (l *rescanSkillLoader) BuildCatalog() string    { return "" }
func (l *rescanSkillLoader) Get(string) *skill.Skill { return nil }
func (l *rescanSkillLoader) All() []*skill.Skill     { return l.skills }
func (l *rescanSkillLoader) Scan() []error           { l.scanned++; return nil }

func TestSkillsRescan(t *testing.T) {
	srv := newTestGateway(t, "secret")
	loader := &rescanSkillLoader{skills: []*skill.Skill{{Name: "a"}, {Name: "b"}}}
	srv.skillLoader = loader

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/skills/rescan", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if loader.scanned != 1 {
		t.Errorf("Scan called %d times, want 1", loader.scanned)
	}
	if int(body["count"].(float64)) != 2 {
		t.Errorf("count = %v, want 2", body["count"])
	}
}

func TestSkillsRescan_NoLoader(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/skills/rescan", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
}

func TestAcceptLearningSkillInstallsAndRescans(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	loader := &rescanSkillLoader{}
	srv.skillLoader = loader

	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)
	p, err := store.Add(learning.Proposal{
		AgentID: "agent-a",
		Kind:    "skill",
		Title:   "Installable skill draft: morning-brief",
		Content: `---
name: morning-brief
description: "Prepare a reusable morning market brief."
---

# Morning Brief

Use this skill to prepare a short morning market brief.
`,
		Meta: map[string]string{"skill_name": "morning-brief"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/learning/proposals/"+p.ID+"/accept", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("accept status = %d body=%v", status, body)
	}
	if loader.scanned != 1 {
		t.Fatalf("Scan called %d times, want 1", loader.scanned)
	}
	path := filepath.Join(workspace, "skills", "morning-brief", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
	got, err := store.List("agent-a", learning.StatusAccepted, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Meta["installed_path"] != path {
		t.Fatalf("accepted proposal metadata = %#v, want installed_path %s", got, path)
	}
}

func TestUpdateLearningSkillProposalBeforeAccept(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	srv.skillLoader = &rescanSkillLoader{}
	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)
	p, err := store.Add(learning.Proposal{
		AgentID: "agent-a",
		Kind:    "skill",
		Title:   "Installable skill draft: rough",
		Content: `---
name: rough
description: "Rough draft."
---

# Rough
`,
		Meta: map[string]string{"skill_name": "rough"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	edit := `{"title":"Installable skill draft: polished","meta":{"skill_name":"polished"},"content":"---\nname: polished\ndescription: \"Polished draft.\"\n---\n\n# Polished\n\nFinal instructions.\n"}`
	status, body := gatewayJSON(t, srv, http.MethodPatch, "/api/v1/learning/proposals/"+p.ID, "secret", edit)
	if status != http.StatusOK {
		t.Fatalf("patch status = %d body=%v", status, body)
	}
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/learning/proposals/"+p.ID+"/accept", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("accept status = %d body=%v", status, body)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "skills", "polished", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if !strings.Contains(string(raw), "Final instructions.") {
		t.Fatalf("installed skill did not use edited content:\n%s", raw)
	}
}
