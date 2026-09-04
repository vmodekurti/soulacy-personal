package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func deployableAgent() agent.Definition {
	def := certifiableAgent()
	def.LLM.BaseURL = "https://llm.internal:8443/v1"
	return def
}

func newTestDeploymentStore(t *testing.T) *DeploymentStore {
	t.Helper()
	return NewDeploymentStore(filepath.Join(t.TempDir(), "deployments"))
}

// TestDeploymentRecordCapturesEveryRequiredFacet is the acceptance criterion in
// one test: a deployment must record the workflow version, the rules version,
// the provider configuration and the test evidence. A record missing any of
// these cannot answer "what changed?" during an incident.
func TestDeploymentRecordCapturesEveryRequiredFacet(t *testing.T) {
	def := deployableAgent()
	cert := Certify(certifiableInput(), certAt)
	rec, err := NewDeploymentRecord(def, DefaultSOULRules, &cert, "alice", "first deploy")
	if err != nil {
		t.Fatalf("NewDeploymentRecord: %v", err)
	}

	// 1. Workflow version — the content hash, not the (forgettable) semver.
	if rec.WorkflowHash == "" {
		t.Error("deployment must record a workflow content hash")
	}
	if rec.WorkflowHash != WorkflowHash(def) {
		t.Errorf("workflow hash must match the deployed definition: %q", rec.WorkflowHash)
	}
	// 2. Rules version.
	if rec.RulesVersion == "" || rec.RulesVersion != RulesVersion(DefaultSOULRules) {
		t.Errorf("deployment must pin the rules version, got %q", rec.RulesVersion)
	}
	// 3. Provider configuration.
	if rec.ProviderConfig["llm.provider"] != "anthropic" || rec.ProviderConfig["llm.model"] != "claude-sonnet-4-6" {
		t.Errorf("deployment must record provider/model per role: %v", rec.ProviderConfig)
	}
	// 4. Test evidence.
	if rec.Certification == nil || !rec.Certification.Certified || rec.Certification.RunID != "run-1" {
		t.Errorf("deployment must carry the certification evidence: %+v", rec.Certification)
	}
	if !rec.Certified() {
		t.Error("a record with a passing certification should report Certified()")
	}
	// The definition itself, so rollback never depends on another store.
	got, err := rec.AgentDefinition()
	if err != nil {
		t.Fatalf("AgentDefinition: %v", err)
	}
	if got.ID != def.ID || got.LLM.Model != def.LLM.Model {
		t.Errorf("stored definition must round-trip: %+v", got)
	}
	if rec.DeployedBy != "alice" || rec.Note != "first deploy" {
		t.Errorf("deployment must record who and why: %+v", rec)
	}
}

// TestDeploymentVersionsAreMonotonicAndHistoryIsAppendOnly guards the property
// an audit trail lives or dies by: a new deploy never renumbers or removes an
// old one.
func TestDeploymentVersionsAreMonotonicAndHistoryIsAppendOnly(t *testing.T) {
	store := newTestDeploymentStore(t)
	def := deployableAgent()

	var hashes []string
	for i := 0; i < 3; i++ {
		d := def
		d.SystemPrompt = strings.Repeat("x", i+1) // each deploy is different content
		rec, err := NewDeploymentRecord(d, DefaultSOULRules, nil, "alice", "")
		if err != nil {
			t.Fatalf("build record %d: %v", i, err)
		}
		// A caller-supplied version must be ignored — only the store may assign.
		rec.Version = 99
		stored, err := store.Record(rec)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if stored.Version != i+1 {
			t.Fatalf("version %d = %d, want %d (monotonic, store-assigned)", i, stored.Version, i+1)
		}
		if stored.DeployedAt.IsZero() {
			t.Fatal("the store must stamp DeployedAt")
		}
		hashes = append(hashes, stored.WorkflowHash)
	}

	hist, err := store.History(def.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("history length = %d, want 3 (append-only)", len(hist))
	}
	for i, rec := range hist {
		if rec.Version != i+1 {
			t.Errorf("history[%d].Version = %d, want %d", i, rec.Version, i+1)
		}
		if rec.WorkflowHash != hashes[i] {
			t.Errorf("history[%d] hash changed: %q != %q", i, rec.WorkflowHash, hashes[i])
		}
	}

	latest, err := store.Latest(def.ID)
	if err != nil || latest.Version != 3 {
		t.Errorf("Latest = v%d (%v), want v3", latest.Version, err)
	}
	prev, err := store.Previous(def.ID)
	if err != nil || prev.Version != 2 {
		t.Errorf("Previous = v%d (%v), want v2", prev.Version, err)
	}
}

// TestRollbackRestoresPreviousDefinitionAsANewVersion: rolling back must put
// the old content back AND leave the bad version in history, because deleting
// it would erase the evidence of what caused the incident.
func TestRollbackRestoresPreviousDefinitionAsANewVersion(t *testing.T) {
	store := newTestDeploymentStore(t)
	def := deployableAgent()

	good := def
	good.SystemPrompt = "the version that worked"
	goodCert := Certify(certifiableInput(), certAt)
	rec, _ := NewDeploymentRecord(good, DefaultSOULRules, &goodCert, "alice", "good")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record good: %v", err)
	}

	bad := def
	bad.SystemPrompt = "the version that broke production"
	rec, _ = NewDeploymentRecord(bad, DefaultSOULRules, nil, "bob", "bad")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record bad: %v", err)
	}

	rolled, err := store.Rollback(def.ID, "carol")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Version != 3 {
		t.Errorf("rollback must APPEND a new version, got v%d", rolled.Version)
	}
	if !rolled.IsRollback() || rolled.RolledBackFrom != 2 || rolled.RolledBackTo != 1 {
		t.Errorf("rollback must record what it undid: %+v", rolled)
	}
	if rolled.DeployedBy != "carol" {
		t.Errorf("rollback must record who performed it, got %q", rolled.DeployedBy)
	}
	restored, err := rolled.AgentDefinition()
	if err != nil {
		t.Fatalf("restored definition: %v", err)
	}
	if restored.SystemPrompt != "the version that worked" {
		t.Errorf("rollback must restore the PREVIOUS definition, got %q", restored.SystemPrompt)
	}
	if restored.SystemPrompt == bad.SystemPrompt {
		t.Error("rollback restored the broken version")
	}
	// The certification travels with the content it certified.
	if rolled.Certification == nil || !rolled.Certification.Certified {
		t.Error("rolling back to a certified version must restore a certified deployment")
	}

	hist, err := store.History(def.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("history length = %d, want 3 — rollback must not rewrite history", len(hist))
	}
	badStored, err := hist[1].AgentDefinition()
	if err != nil || badStored.SystemPrompt != bad.SystemPrompt {
		t.Error("the rolled-back-from version must remain in history for the incident review")
	}
}

func TestRollbackWithNoPreviousVersionErrorsCleanly(t *testing.T) {
	store := newTestDeploymentStore(t)

	// Never deployed.
	if _, err := store.Rollback("ghost", "alice"); !errors.Is(err, ErrNoDeployment) {
		t.Errorf("rollback of an undeployed agent = %v, want ErrNoDeployment", err)
	}
	if _, err := store.Latest("ghost"); !errors.Is(err, ErrNoDeployment) {
		t.Errorf("Latest of an undeployed agent = %v, want ErrNoDeployment", err)
	}

	// Deployed exactly once: there is no earlier version to restore, and
	// "rolling back to nothing" would leave the agent with no definition at all.
	def := deployableAgent()
	rec, _ := NewDeploymentRecord(def, DefaultSOULRules, nil, "alice", "")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	_, err := store.Rollback(def.ID, "alice")
	if !errors.Is(err, ErrNoPreviousDeployment) {
		t.Errorf("rollback of a first deployment = %v, want ErrNoPreviousDeployment", err)
	}
	hist, _ := store.History(def.ID)
	if len(hist) != 1 {
		t.Errorf("a failed rollback must not touch history, got %d records", len(hist))
	}
}

// TestNoSecretsArePersistedInProviderConfig reads the raw bytes on disk,
// because that — not the in-memory struct — is what ends up in a support bundle.
func TestNoSecretsArePersistedInProviderConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deployments")
	store := NewDeploymentStore(dir)

	var (
		anthropicKey = "sk-ant-" + strings.Repeat("a", 32)
		slackToken   = "xoxb-" + strings.Repeat("1-", 8) + strings.Repeat("a", 16)
		awsKey       = "AKIA" + strings.Repeat("A", 16)
		hexToken     = strings.Repeat("aA1", 22)
		urlWithCreds = "https://admin:hunter2pass@llm.internal/v1"
	)

	def := deployableAgent()
	rec, err := NewDeploymentRecord(def, DefaultSOULRules, nil, "alice", "")
	if err != nil {
		t.Fatalf("NewDeploymentRecord: %v", err)
	}
	// Simulate a careless caller stuffing real credentials into the map: the
	// store must redact on the way IN, not trust its callers.
	rec.ProviderConfig["llm.anthropic.api_key"] = anthropicKey
	rec.ProviderConfig["channel_token"] = slackToken
	rec.ProviderConfig["aws_access"] = awsKey
	rec.ProviderConfig["opaque"] = hexToken
	rec.ProviderConfig["endpoint"] = urlWithCreds

	if _, err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, def.ID+".json"))
	if err != nil {
		t.Fatalf("read persisted history: %v", err)
	}
	text := string(raw)
	for _, secret := range []string{anthropicKey, slackToken, awsKey, hexToken, "hunter2pass", "s3cr3tT0ken", "abcd1234"} {
		if strings.Contains(text, secret) {
			t.Errorf("a secret-looking value was persisted to disk: %q", secret)
		}
	}

	stored, err := store.Latest(def.ID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	// Redaction must not be so eager that it destroys the field's purpose.
	if stored.ProviderConfig["llm.provider"] != "anthropic" {
		t.Errorf("provider NAME must survive redaction: %v", stored.ProviderConfig)
	}
	if stored.ProviderConfig["llm.model"] != "claude-sonnet-4-6" {
		t.Errorf("model NAME must survive redaction: %v", stored.ProviderConfig)
	}
	// base_url is kept as an endpoint, stripped of userinfo and query.
	if got := stored.ProviderConfig["llm.base_url"]; got != "https://llm.internal:8443/v1" {
		t.Errorf("base_url = %q, want the endpoint with credentials stripped", got)
	}
	if stored.ProviderConfig["llm.anthropic.api_key"] != redactedValue {
		t.Errorf("a credential key must read as redacted, got %q", stored.ProviderConfig["llm.anthropic.api_key"])
	}
}

// TestRedactionKeepsRealModelNames is the false-positive guard: a redactor that
// eats "claude-3-5-sonnet-20241022" destroys the very field it protects.
func TestRedactionKeepsRealModelNames(t *testing.T) {
	keep := map[string]string{
		"llm.model":              "claude-3-5-sonnet-20241022",
		"llm.provider":           "anthropic",
		"agent.researcher.model": "gpt-4o-2024-08-06",
		"reasoning.strategy":     "plan_execute",
		"llm.base_url":           "http://localhost:11434",
	}
	out := RedactProviderConfig(keep)
	for k, want := range keep {
		if out[k] != want {
			t.Errorf("%s was redacted (%q) but is not a secret", k, out[k])
		}
	}
	if RedactProviderConfig(nil) != nil {
		t.Error("nil in, nil out")
	}
}

func TestProviderConfigCoversPeerAgentRoles(t *testing.T) {
	def := deployableAgent()
	def.Workflow.Nodes = append(def.Workflow.Nodes,
		sdkr.FlowNode{ID: "summarize", Kind: "agent", Agent: "summarizer"})
	peers := map[string]agent.Definition{
		"summarizer": {ID: "summarizer", LLM: agent.LLMConfig{Provider: "ollama", Model: "llama3"}},
	}
	cfg := ProviderConfigFromDefinitions(def, peers)
	if cfg["agent.summarizer.provider"] != "ollama" || cfg["agent.summarizer.model"] != "llama3" {
		t.Errorf("provider config must cover peer roles, got %v", cfg)
	}
}

// TestScheduleReadinessHasNoOpinionAboutUndeployedAgents: the gate must not
// block hand-written YAML agents that never passed through Studio.
func TestScheduleReadinessHasNoOpinionAboutUndeployedAgents(t *testing.T) {
	store := newTestDeploymentStore(t)
	r := store.ScheduleReadiness("hand-written")
	if r.Deployed || r.Blocked {
		t.Errorf("an undeployed agent must yield no opinion, got %+v", r)
	}
}

func TestScheduleReadinessBlocksUncertifiedDeployments(t *testing.T) {
	store := newTestDeploymentStore(t)
	def := deployableAgent()

	// Deployed with NO evidence at all.
	rec, _ := NewDeploymentRecord(def, DefaultSOULRules, nil, "alice", "")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	r := store.ScheduleReadiness(def.ID)
	if !r.Deployed || !r.Blocked {
		t.Fatalf("a deployment with no certification must block, got %+v", r)
	}
	if len(r.Failed) == 0 || r.Failed[0].Fix == "" || r.Failed[0].Action == "" {
		t.Errorf("a block must name an actionable fix: %+v", r.Failed)
	}

	// Deployed with a FAILING certification.
	in := certifiableInput()
	in.LastRealRun = &RealRunEvidence{RunID: "dry-1", Dry: true, Succeeded: true, OutcomeMet: true}
	failing := Certify(in, certAt)
	rec, _ = NewDeploymentRecord(def, DefaultSOULRules, &failing, "alice", "")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	r = store.ScheduleReadiness(def.ID)
	if !r.Blocked {
		t.Fatal("a failing certification must block scheduling")
	}
	found := false
	for _, f := range r.Failed {
		if f.ID == "real_run" {
			found = true
		}
	}
	if !found {
		t.Errorf("the block must name the failed requirement: %+v", r.Failed)
	}

	// Re-deploying with a PASSING certification clears the block with no
	// restart and no cache to invalidate — the store is re-read every call.
	passing := Certify(certifiableInput(), certAt)
	rec, _ = NewDeploymentRecord(def, DefaultSOULRules, &passing, "alice", "")
	if _, err := store.Record(rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	if r = store.ScheduleReadiness(def.ID); r.Blocked {
		t.Errorf("a certified deployment must not block: %+v", r)
	}
	if r.Version != 3 {
		t.Errorf("readiness must read the CURRENT deployment, got v%d", r.Version)
	}
}

// TestScheduleReadinessFailsClosedOnCorruptHistory: an unreadable history must
// never read as "never deployed", or an uncertified agent would be handed a
// clean bill of health and its schedule would fire.
func TestScheduleReadinessFailsClosedOnCorruptHistory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deployments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewDeploymentStore(dir)
	r := store.ScheduleReadiness("broken")
	if !r.Deployed || !r.Blocked {
		t.Fatalf("a corrupt deployment history must fail closed, got %+v", r)
	}
	if _, err := store.History("broken"); err == nil {
		t.Error("History must surface the parse failure, not swallow it")
	}
}

func TestDeploymentStoreRejectsTraversalAgentIDs(t *testing.T) {
	store := newTestDeploymentStore(t)
	for _, id := range []string{"../escape", "a/b", ".", "..", ""} {
		if _, err := store.Record(DeploymentRecord{AgentID: id}); err == nil {
			t.Errorf("agent id %q should have been rejected", id)
		}
	}
}

func TestDeploymentRecordJSONRoundTrip(t *testing.T) {
	def := deployableAgent()
	cert := Certify(certifiableInput(), certAt)
	rec, err := NewDeploymentRecord(def, DefaultSOULRules, &cert, "alice", "note")
	if err != nil {
		t.Fatalf("NewDeploymentRecord: %v", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back DeploymentRecord
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.WorkflowHash != rec.WorkflowHash || back.RulesVersion != rec.RulesVersion {
		t.Errorf("record must survive a JSON round trip: %+v", back)
	}
}
