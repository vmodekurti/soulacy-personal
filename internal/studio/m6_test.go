// m6_test.go — Studio M6 backend tests (Stories S6.1/S6.2/S6.3): starter
// templates, the draft library store, and per-node re-describe.
package studio

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

// --- S6.1 templates ---

func TestTemplates_NonEmpty(t *testing.T) {
	tmpls := Templates()
	if len(tmpls) == 0 {
		t.Fatal("Templates() returned no templates")
	}
	seen := map[string]bool{}
	for _, tm := range tmpls {
		if tm.ID == "" || tm.Name == "" || tm.Description == "" {
			t.Errorf("template %+v missing id/name/description", tm)
		}
		if seen[tm.ID] {
			t.Errorf("duplicate template id %q", tm.ID)
		}
		seen[tm.ID] = true
	}
}

func TestTemplates_WorkflowsCompile(t *testing.T) {
	for _, tm := range Templates() {
		if _, err := reasoning.CompileFlow(tm.Workflow.spec()); err != nil {
			t.Errorf("template %q workflow failed CompileFlow: %v", tm.ID, err)
		}
		// And via the helper, which the gateway/tests can also rely on.
		if err := tm.compiles(); err != nil {
			t.Errorf("template %q compiles() reported: %v", tm.ID, err)
		}
	}
}

// --- S6.2 draft library ---

func TestDraftLibrary_SaveListLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	wf := Templates()[0].Workflow

	id, err := SaveDraft(root, "My First Draft", wf)
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if id == "" {
		t.Fatal("SaveDraft returned empty id")
	}
	if !strings.HasPrefix(id, "my-first-draft-") {
		t.Errorf("id %q does not start with the expected slug", id)
	}

	metas, err := ListDrafts(root)
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(metas))
	}
	if metas[0].ID != id || metas[0].Name != "My First Draft" {
		t.Errorf("meta mismatch: %+v", metas[0])
	}
	if metas[0].Updated == "" {
		t.Error("meta.Updated is empty")
	}

	loaded, err := LoadDraft(root, id)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if loaded.ID != id || loaded.Name != "My First Draft" {
		t.Errorf("loaded mismatch: %+v", loaded)
	}
	gotJSON, _ := json.Marshal(loaded.Workflow)
	wantJSON, _ := json.Marshal(wf)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("workflow round-trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestDraftLibrary_IdempotentOverwrite(t *testing.T) {
	root := t.TempDir()
	wf := Templates()[0].Workflow

	id1, err := SaveDraft(root, "Same Name", wf)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	id2, err := SaveDraft(root, "Same Name", wf)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if id1 != id2 {
		t.Errorf("identical name+workflow should yield the same id; got %q and %q", id1, id2)
	}
	metas, _ := ListDrafts(root)
	if len(metas) != 1 {
		t.Errorf("idempotent re-save should leave 1 draft, got %d", len(metas))
	}

	// A different workflow under the same name gets a distinct id.
	wf2 := Templates()[1].Workflow
	id3, err := SaveDraft(root, "Same Name", wf2)
	if err != nil {
		t.Fatalf("third save: %v", err)
	}
	if id3 == id1 {
		t.Error("different workflow under same name should get a distinct id")
	}
	metas, _ = ListDrafts(root)
	if len(metas) != 2 {
		t.Errorf("expected 2 drafts after a differing save, got %d", len(metas))
	}
}

func TestDraftLibrary_Delete(t *testing.T) {
	root := t.TempDir()
	id, err := SaveDraft(root, "Doomed", Templates()[0].Workflow)
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := DeleteDraft(root, id); err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}
	if _, err := LoadDraft(root, id); err == nil {
		t.Error("LoadDraft after delete should error")
	}
	// Deleting again is a not-found error.
	if err := DeleteDraft(root, id); err == nil {
		t.Error("deleting a missing draft should error")
	}
	metas, _ := ListDrafts(root)
	if len(metas) != 0 {
		t.Errorf("expected 0 drafts after delete, got %d", len(metas))
	}
}

func TestDraftLibrary_ListMissingRootIsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist-yet")
	metas, err := ListDrafts(root)
	if err != nil {
		t.Fatalf("ListDrafts on missing root: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("expected empty list, got %d", len(metas))
	}
}

func TestDraftLibrary_BadIDRejected(t *testing.T) {
	root := t.TempDir()
	bad := []string{
		"../escape",
		"a/b",
		`a\b`,
		"..",
		".",
		"",
		"foo/../../etc/passwd",
	}
	for _, id := range bad {
		if _, err := LoadDraft(root, id); err == nil {
			t.Errorf("LoadDraft(%q) should be rejected", id)
		}
		if err := DeleteDraft(root, id); err == nil {
			t.Errorf("DeleteDraft(%q) should be rejected", id)
		}
	}
	// A NUL byte must also be rejected.
	if _, err := LoadDraft(root, "foo\x00bar"); err == nil {
		t.Error("LoadDraft with NUL byte should be rejected")
	}
}

func TestDraftLibrary_EmptyNameRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveDraft(root, "   ", Templates()[0].Workflow); err == nil {
		t.Error("SaveDraft with blank name should error")
	}
}

// --- S6.3 refine ---

// editedDraftJSON is the canonical first template with its agent node's input
// changed (as a fake model would return after "use bullet points").
const editedDraftJSON = `{
  "name": "Weekday HN Digest",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{\"url\":\"https://hacker-news.firebaseio.com/v0/topstories.json\"}", "output": "stories", "x": 0, "y": 0 },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "Summarize the top 5 as bullet points: {{.stories}}", "output": "summary", "x": 220, "y": 0 }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`

func TestRefine_ChangesNodeAndStaysValid(t *testing.T) {
	llm := fakeLLM{out: editedDraftJSON}
	orig := scheduledDigestTemplate().Workflow

	updated, err := Refine(context.Background(), llm, orig, "summarize", "summarize as bullet points")
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}

	// The targeted node's input changed.
	var got string
	for _, n := range updated.Flow.Nodes {
		if n.ID == "summarize" {
			got = n.Input
		}
	}
	if !strings.Contains(got, "bullet points") {
		t.Errorf("summarize node input not updated; got %q", got)
	}

	// The refined flow still validates.
	if _, err := reasoning.CompileFlow(updated.spec()); err != nil {
		t.Errorf("refined flow should still compile: %v", err)
	}
}

func TestRefine_UnknownNodeRejected(t *testing.T) {
	llm := fakeLLM{out: editedDraftJSON}
	orig := scheduledDigestTemplate().Workflow
	if _, err := Refine(context.Background(), llm, orig, "no-such-node", "do something"); err == nil {
		t.Error("Refine with unknown nodeId should error")
	}
}

func TestRefine_InvalidModelOutputRejected(t *testing.T) {
	// Model returns a draft whose edge points at a node that doesn't exist —
	// must be rejected, not returned.
	bad := `{
  "name": "Broken",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * *" } },
  "flow": {
    "nodes": [ { "id": "summarize", "kind": "agent", "agent": "summarizer" } ],
    "edges": [ { "from": "summarize", "to": "ghost" } ],
    "entry": "summarize"
  }
}`
	llm := fakeLLM{out: bad}
	orig := scheduledDigestTemplate().Workflow
	if _, err := Refine(context.Background(), llm, orig, "summarize", "break it"); err == nil {
		t.Error("Refine with an invalid resulting flow should error")
	}
}

func TestRefine_EmptyInstructionRejected(t *testing.T) {
	llm := fakeLLM{out: editedDraftJSON}
	orig := scheduledDigestTemplate().Workflow
	if _, err := Refine(context.Background(), llm, orig, "summarize", "  "); err == nil {
		t.Error("Refine with blank instruction should error")
	}
}
