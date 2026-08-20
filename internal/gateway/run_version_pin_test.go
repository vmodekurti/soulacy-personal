// run_version_pin_test.go — MU-028 criterion 4: "agent deployment references
// an immutable definition version."
//
// The pin already existed. runs.Run.AgentVersion says in its own comment that
// it pins what actually ran so that "an agent edited later must not change
// what this run means", and it was filled from agent.Definition.Version — a
// string the AUTHOR types into SOUL.yaml. That value is usually empty, is
// never updated by an edit, and is fully controlled by whoever wrote the file.
// The field asked the right question and recorded an answer that could not
// change when the definition did.
package gateway

import (
	"context"
	"net/http"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

// The sharp case, stated as the trap it is: an authored `version:` string is
// not a version. Changing it must not change the pin, and changing what the
// agent actually DOES must.
func TestTheVersionPinFollowsContentNotTheAuthoredString(t *testing.T) {
	base := &agent.Definition{ID: "bot", Name: "Bot", SystemPrompt: "be helpful", Version: "1.0.0"}

	relabelled := *base
	relabelled.Version = "9.9.9"
	if relabelled.ContentVersion() == base.ContentVersion() {
		t.Fatal("the authored version string is part of the definition, so editing it should change the token")
	}

	// The failure the old pin had: an edit that changes behaviour, with the
	// authored label left alone. Under `def.Version` these two are the same
	// "version", and the run record cannot tell them apart.
	edited := *base
	edited.SystemPrompt = "be terse"
	if edited.ContentVersion() == base.ContentVersion() {
		t.Fatal("editing the system prompt did not change the version token — a run pinned to it says nothing")
	}
	if edited.Version != base.Version {
		t.Fatal("this test is not exercising the case it describes")
	}

	// And the overwhelmingly common case: no authored version at all. Every
	// run of such an agent was pinned to "".
	unlabelled := &agent.Definition{ID: "bot", Name: "Bot", SystemPrompt: "be helpful"}
	if unlabelled.Version != "" {
		t.Fatal("fixture is wrong")
	}
	if unlabelled.ContentVersion() == "" {
		t.Fatal("an agent with no authored version still has no identity")
	}
}

// Content-derived, not allocated: two processes holding the same definition
// must agree without coordinating, and where the file happens to sit must not
// be part of its identity.
func TestTheVersionTokenIsStableAcrossProcessesAndLocations(t *testing.T) {
	a := &agent.Definition{ID: "bot", Name: "Bot", SystemPrompt: "be helpful"}
	b := &agent.Definition{ID: "bot", Name: "Bot", SystemPrompt: "be helpful"}
	b.SourcePath = "/somewhere/else/SOUL.yaml"
	if a.ContentVersion() != b.ContentVersion() {
		t.Fatal("two replicas disagree about the identity of identical definitions")
	}
}

// One identity, not two that agree by coincidence. The ETag a client edits
// against and the token a run is pinned to have to be comparable, or "did the
// thing that ran differ from what is there now" is unanswerable.
func TestTheETagAndTheRunPinAreTheSameIdentity(t *testing.T) {
	def := &agent.Definition{ID: "bot", Name: "Bot", SystemPrompt: "be helpful"}
	etag := resourceETag(def)
	if etag == "" {
		t.Fatal("no etag")
	}
	if etag != `"`+def.ContentVersion()+`"` {
		t.Fatalf("etag %s and run pin %s are different identities", etag, def.ContentVersion())
	}
}

// At the production call site, not just on the helper. A ContentVersion method
// that works proves nothing about whether handleSubmitRun uses it, and
// "asserted the helper instead of the call site" is precisely how a guard on
// this branch has passed for the wrong reason before.
func TestASubmittedRunIsPinnedToTheDefinitionThatRanIt(t *testing.T) {
	srv, store := runsGateway(t)
	seedAgent(t, srv)

	before := srv.loader.GetInWorkspace(runtime.PersonalWorkspaceID, "contract-agent")
	if before == nil {
		t.Fatal("seed agent missing")
	}
	wantFirst := before.ContentVersion()

	status, body := postRun(t, srv, `{"agent_id":"contract-agent","payload":{"prompt":"go"}}`, "")
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d: %v", status, body)
	}
	firstID, _ := body["run_id"].(string)
	first, err := store.Get(context.Background(), runtime.PersonalWorkspaceID, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentVersion != wantFirst {
		t.Fatalf("run pinned to %q, want the definition's content version %q", first.AgentVersion, wantFirst)
	}

	// Now edit the agent in a way that changes what it does but leaves the
	// authored `version:` field untouched — the case the old pin could not
	// see at all.
	status, edited := gatewayJSON(t, srv, http.MethodPut, "/api/v1/agents/contract-agent", "secret",
		`{"name":"Contract Agent","system_prompt":"be terse","enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("edit = %d: %v", status, edited)
	}

	after := srv.loader.GetInWorkspace(runtime.PersonalWorkspaceID, "contract-agent")
	if after == nil {
		t.Fatal("agent vanished")
	}
	if after.ContentVersion() == wantFirst {
		t.Fatalf("an edit that changed what the agent does left the version at %q — "+
			"a run pinned to it cannot tell the two definitions apart", wantFirst)
	}
	if after.Version != before.Version {
		t.Fatal("this test is not exercising the case it describes: the authored version string moved too")
	}
	// And the first run's pin is unchanged by the edit, which is the property
	// the field exists for: asserted by re-reading the stored row rather than
	// the value captured above, so a store that rewrote history would fail.
	reread, err := store.Get(context.Background(), runtime.PersonalWorkspaceID, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.AgentVersion != wantFirst {
		t.Fatalf("the earlier run's pin moved to %q when the agent was edited", reread.AgentVersion)
	}
}

// The join criterion 4 is actually for: a run names a version, and the bytes
// that produced it become retrievable the moment the definition is replaced.
// Without a shared token the only way to line them up is by timestamp — and a
// timestamp is exactly what a backup restore rewrites, which is why the
// snapshot sidecar exists in the first place.
func TestARunsVersionIsFindableInTheAgentsHistory(t *testing.T) {
	srv, store := runsGateway(t)
	seedAgent(t, srv)

	status, body := postRun(t, srv, `{"agent_id":"contract-agent","payload":{"prompt":"go"}}`, "")
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d: %v", status, body)
	}
	runID, _ := body["run_id"].(string)
	run, err := store.Get(context.Background(), runtime.PersonalWorkspaceID, runID)
	if err != nil {
		t.Fatal(err)
	}

	// Replacing the definition is what makes the executed bytes retrievable.
	status, edited := gatewayJSON(t, srv, http.MethodPut, "/api/v1/agents/contract-agent", "secret",
		`{"name":"Contract Agent","system_prompt":"be terse","enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("edit = %d: %v", status, edited)
	}

	versions, err := srv.loader.AgentVersionsInWorkspace(runtime.PersonalWorkspaceID, "contract-agent")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no snapshots recorded: %v %v", versions, err)
	}
	found := false
	for _, v := range versions {
		if v.ContentVersion == run.AgentVersion {
			found = true
		}
	}
	if !found {
		var got []string
		for _, v := range versions {
			got = append(got, v.ContentVersion)
		}
		t.Fatalf("no snapshot carries the version run %s executed (%q); history has %v", runID, run.AgentVersion, got)
	}
}

// The failure that made the token useless before it was ever contended.
//
// A definition reaches memory two ways: parsed from its own SOUL.yaml at boot,
// or decoded from a JSON request body at create time. Under a JSON-derived
// hash those produced DIFFERENT tokens for identical content, because a nil
// slice is `null` in JSON and `[]` in YAML and parsing `[]` back gives an
// empty non-nil slice. Consequences, both invisible until two people are
// editing — which is the only situation the token exists for:
//
//   - every client's held version was invalidated by a gateway restart,
//     producing a conflict against an editor who does not exist; and
//   - a run's pin could not be joined to the snapshot holding the bytes that
//     produced it, because the two were hashed from different representations.
func TestTheVersionSurvivesTheRoundTripThroughItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})

	// As created through the API: slices are nil.
	fromAPI := &agent.Definition{ID: "bot", Name: "Bot", Enabled: true}
	fromAPI.LLM.Provider = "openai"
	if err := loader.UpsertInWorkspace("ws_a", dir, fromAPI, "usr_alice"); err != nil {
		t.Fatal(err)
	}
	live := loader.GetInWorkspace("ws_a", "bot")
	if live == nil {
		t.Fatal("agent not stored")
	}

	// As read back at boot: the same file, parsed.
	raw, err := os.ReadFile(live.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var fromDisk agent.Definition
	if err := yaml.Unmarshal(raw, &fromDisk); err != nil {
		t.Fatal(err)
	}

	if live.ContentVersion() != fromDisk.ContentVersion() {
		t.Fatalf("the same agent has version %q in memory and %q after a restart — "+
			"every client's held version would be invalidated by a restart",
			live.ContentVersion(), fromDisk.ContentVersion())
	}
}
