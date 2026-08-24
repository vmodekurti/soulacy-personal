// agent_write_conflict_test.go — the behavioural half of MU-028 criterion 2.
//
// agent_write_guard_test.go proves no agent-mutating handler can be written
// without a decision. These prove the decision is the right one at the five
// handlers that previously had none, and — more importantly — that a refused
// write left the stored definition alone. A 409 that still wrote is the
// original bug wearing an error code.
package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/pkg/agent"
)

// teamAgentApp mounts the real agent handlers behind the real workspace
// context middleware in Team mode.
//
// The claims injection stands in for JWT verification only. Everything the
// guard depends on — workspaceContextMW deriving the verified identity, the
// handler resolving its scope through s.agents(c), the loader writing into
// that workspace's own directory — is the production path. Driving the guard
// helpers directly would prove the helpers work and say nothing about whether
// any handler calls them, which is the shape of test that has passed for the
// wrong reason repeatedly on this branch.
//
// actor is a pointer so a test can change who the next request is from,
// which is what makes "somebody else changed this" testable at all.
func teamAgentApp(t *testing.T, actor *string) (*Server, *fiber.App) {
	srv, app, _ := teamAgentAppWithLLM(t, actor)
	return srv, app
}

// teamAgentAppWithLLM additionally hands back the fake provider, so a test can
// drive the Builder through its real chat turn rather than reaching into the
// engine's unexported session map. Driving the real path matters here: the
// deploy handler refuses outright when there is no session, so a test that
// skips the chat turn proves nothing about the write it never reaches.
func teamAgentAppWithLLM(t *testing.T, actor *string) (*Server, *fiber.App, *fakeLLMProvider) {
	t.Helper()
	srv, provider := newTestGatewayWithLLM(t, "secret")
	// The fake provider registers under "test"; the config's default is
	// "openai". The Builder resolves its provider from the config, so without
	// this the chat turn fails on an unknown provider before it can reach the
	// write this test is about.
	srv.config().LLM.DefaultProvider = provider.ID()
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_team", WorkspaceID: "ws_team", MembershipID: "mem_team", Role: "owner",
	}})

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: *actor},
			Role:             "owner", Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_" + *actor,
		})
		c.Locals("request_id", "req-"+*actor)
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	app.Get("/agents", srv.handleListAgents)
	app.Post("/agents", srv.handleCreateAgent)
	app.Get("/agents/:id", srv.handleGetAgent)
	app.Put("/agents/:id", srv.handleUpdateAgent)
	app.Delete("/agents/:id", srv.handleDeleteAgent)
	app.Post("/agents/:id/rollback", srv.handleRollbackAgent)
	app.Post("/studio/save-yaml", srv.handleStudioSaveYAML)
	app.Post("/studio/save", srv.handleStudioSave)
	app.Get("/studio/agents", srv.handleStudioListAgents)
	app.Get("/studio/agents/:id", srv.handleStudioLoadAgent)
	app.Post("/builder/deploy", srv.handleBuilderDeploy)
	app.Post("/agents/import", srv.handleImportAgentPackage)
	app.Post("/builder/chat", srv.handleBuilderChat)
	return srv, app, provider
}

func agentRequest(t *testing.T, app *fiber.App, method, path, ifMatch, body string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set(fiber.HeaderIfMatch, ifMatch)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	etag := resp.Header.Get(fiber.HeaderETag)
	decoded := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded, etag
}

// storedName reads the definition the LOADER holds, not the one a response
// echoed. A handler that refuses and writes anyway returns the same 409 as one
// that refuses correctly; only the store can tell them apart.
func storedName(t *testing.T, srv *Server, id string) string {
	t.Helper()
	def := srv.loader.GetInWorkspace("ws_team", id)
	if def == nil {
		return ""
	}
	return def.Name
}

// THE BUG. handleCreateAgent called Upsert, so POST /agents with an id that
// already existed replaced that agent and answered 201 Created. Two members
// naming a bot the same obvious thing is not an exotic race.
func TestCreatingAnAgentThatAlreadyExistsDoesNotReplaceIt(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)

	if status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"support-bot","name":"Alice's bot","enabled":true}`); status != http.StatusCreated {
		t.Fatalf("first create = %d %v", status, body)
	}

	actor = "usr_bob"
	status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"support-bot","name":"Bob's bot","enabled":true}`)
	if status != http.StatusConflict {
		t.Fatalf("re-creating an existing id = %d, want 409: %v", status, body)
	}
	if got := storedName(t, srv, "support-bot"); got != "Alice's bot" {
		t.Fatalf("the refused create still overwrote the agent: name = %q", got)
	}
	if body["remedy"] == nil || !strings.Contains(body["remedy"].(string), "If-Match") {
		t.Fatalf("the 409 does not tell the caller how to replace deliberately: %v", body)
	}
}

// Product invariant 7: a single-user install has nobody to collide with, and
// re-creating an id there is one person overwriting their own work knowingly.
func TestAPersonalDeploymentStillOverwritesOnCreate(t *testing.T) {
	srv := newTestGateway(t, "secret")
	if status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"solo","name":"first","enabled":true}`); status != http.StatusCreated {
		t.Fatalf("first create = %d %v", status, body)
	}
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"solo","name":"second","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("personal re-create = %d, want 201 (invariant 7): %v", status, body)
	}
	if def := srv.loader.GetInWorkspace(runtime.PersonalWorkspaceID, "solo"); def == nil || def.Name != "second" {
		t.Fatalf("personal re-create did not overwrite as it always has: %+v", def)
	}
}

// The other half of the create policy: naming the version you are replacing is
// how you say "yes, replace it" — and a STALE token on that path still loses,
// or versioning would be advisory exactly where a client is least careful.
func TestACreateMayReplaceDeliberatelyButNotStalely(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"dup","name":"first","enabled":true}`)

	_, _, current := agentRequest(t, app, http.MethodGet, "/agents/dup", "", "")
	if current == "" {
		t.Fatal("GET /agents/:id returns no validator, so a client cannot participate")
	}

	status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", `"a-version-from-before"`,
		`{"id":"dup","name":"stale","enabled":true}`)
	if status != http.StatusConflict {
		t.Fatalf("stale create = %d, want 409: %v", status, body)
	}
	if got := storedName(t, srv, "dup"); got != "first" {
		t.Fatalf("a stale create still wrote: name = %q", got)
	}

	if status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", current,
		`{"id":"dup","name":"deliberate","enabled":true}`); status != http.StatusCreated {
		t.Fatalf("deliberate replace = %d, want 201: %v", status, body)
	}
	if got := storedName(t, srv, "dup"); got != "deliberate" {
		t.Fatalf("a deliberate replace did not take effect: name = %q", got)
	}
}

// A delete is the most complete stale write there is — it discards every edit
// made since the deleter last looked, not just the overlapping fields.
func TestADeleteWithAStaleVersionIsRefusedAndTheAgentSurvives(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"doomed","name":"keep me","enabled":true}`)

	status, _, _ := agentRequest(t, app, http.MethodDelete, "/agents/doomed", `"a-version-from-before"`, "")
	if status != http.StatusConflict {
		t.Fatalf("stale delete = %d, want 409", status)
	}
	if srv.loader.GetInWorkspace("ws_team", "doomed") == nil {
		t.Fatal("a refused delete removed the agent anyway")
	}

	// And deleting an id that never existed stays as quiet as it has always
	// been: Delete is idempotent, so an absent agent must not start answering
	// 428 just because the guard was added.
	if status, _, _ := agentRequest(t, app, http.MethodDelete, "/agents/never-existed", "", ""); status != http.StatusNoContent {
		t.Fatalf("deleting an absent agent = %d, want 204", status)
	}
}

// A rollback replaces the whole file with older content, and it is issued by
// someone looking at history rather than at what is live — the most likely
// write to be made from a stale view.
func TestARollbackWithAStaleVersionIsRefused(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"histo","name":"v1","enabled":true}`)
	_, _, v1 := agentRequest(t, app, http.MethodGet, "/agents/histo", "", "")
	agentRequest(t, app, http.MethodPut, "/agents/histo", v1, `{"name":"v2","enabled":true}`)

	versions, err := srv.loader.AgentVersionsInWorkspace("ws_team", "histo")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no snapshot to roll back to: %v %v", versions, err)
	}
	status, _, _ := agentRequest(t, app, http.MethodPost, "/agents/histo/rollback",
		`"a-version-from-before"`, `{"version":"`+versions[0].ID+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("stale rollback = %d, want 409", status)
	}
	if got := storedName(t, srv, "histo"); got != "v2" {
		t.Fatalf("a refused rollback still restored: name = %q", got)
	}
}

// Studio's code view and the REST YAML editor write the same agent, and are
// the pair of surfaces most likely to be open on it at once.
func TestStudioCodeViewRefusesAStaleSave(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"canvas","name":"live","enabled":true}`)

	payload, _ := json.Marshal(map[string]any{"yaml": "id: canvas\nname: clobbered\n"})
	status, _, _ := agentRequest(t, app, http.MethodPost, "/studio/save-yaml",
		`"a-version-from-before"`, string(payload))
	if status != http.StatusConflict {
		t.Fatalf("stale Studio save = %d, want 409", status)
	}
	if got := storedName(t, srv, "canvas"); got != "live" {
		t.Fatalf("a refused Studio save still wrote: name = %q", got)
	}
}

// MU-028 criterion 5. "Somebody else changed this" without saying who is
// unactionable in a team, because the remedy is a conversation.
func TestTheConflictNamesWhoWroteWhatIsThereNow(t *testing.T) {
	actor := "usr_alice"
	_, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"shared","name":"v1","enabled":true}`)
	_, _, v1 := agentRequest(t, app, http.MethodGet, "/agents/shared", "", "")

	// Bob saves against the version he read.
	actor = "usr_bob"
	if status, body, _ := agentRequest(t, app, http.MethodPut, "/agents/shared", v1,
		`{"name":"bob's edit","enabled":true}`); status != http.StatusOK {
		t.Fatalf("bob's save = %d %v", status, body)
	}

	// Carol saves from a form rendered before Bob's save.
	actor = "usr_carol"
	status, body, _ := agentRequest(t, app, http.MethodPut, "/agents/shared", v1,
		`{"name":"carol's edit","enabled":true}`)
	if status != http.StatusConflict {
		t.Fatalf("carol's stale save = %d, want 409", status)
	}
	// Asserted on the field, not on the body containing "usr_bob": the
	// message string carries the same name, so a substring check would pass
	// with last_modified_by emptied.
	if body["last_modified_by"] != "usr_bob" {
		t.Fatalf("last_modified_by = %v, want usr_bob: %v", body["last_modified_by"], body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "usr_bob") {
		t.Fatalf("the message does not name the other actor: %q", msg)
	}
}

// The inversion that is easy to get backwards: a snapshot records the bytes
// that were REPLACED, stamped with the principal that replaced them. So the
// newest snapshot names the author of what is live now, not of the snapshot.
// Reading it the other way names the wrong person, which is worse than naming
// nobody — it sends the loser of a conflict to the wrong conversation.
func TestTheNewestSnapshotNamesTheAuthorOfTheCurrentDefinition(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})

	write := func(actor, name string) {
		def := &agent.Definition{ID: "bot", Name: name, Enabled: true}
		if existing := loader.GetInWorkspace("ws_a", "bot"); existing != nil {
			def.SourcePath = existing.SourcePath
		}
		if err := loader.UpsertInWorkspace("ws_a", dir, def, actor); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	write("usr_alice", "v1")
	write("usr_bob", "v2")

	latest, ok := loader.LatestAgentVersionInWorkspace("ws_a", "bot")
	if !ok {
		t.Fatal("no snapshot recorded")
	}
	if latest.Actor != "usr_bob" {
		t.Fatalf("latest snapshot actor = %q, want usr_bob (who wrote what is live)", latest.Actor)
	}

	// And it cannot answer across tenants: agent ids are unique per workspace,
	// so an id-keyed history lookup would happily name another tenant's editor.
	if _, ok := loader.LatestAgentVersionInWorkspace("ws_b", "bot"); ok {
		t.Fatal("another workspace's history answered for the same agent id")
	}
}

// MU-028 criterion 3's precondition, in both senses. Nothing in the GUI ever
// fetched a single agent — every page edits out of GET /agents — so without a
// validator on the list there was no version any client could send, and
// criterion 2's requirement would have refused every save in a multi-user
// deployment.
//
// Asserted by ROUND TRIP rather than by shape. A `versions` map containing the
// wrong token passes any "does the field exist" check and fails every save,
// which is the worse of the two bugs and the one a shape assertion cannot see.
func TestTheAgentListHandsOutAVersionThatSatisfiesASave(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"listed","name":"v1","enabled":true}`)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()

	versions, _ := listed["versions"].(map[string]any)
	token, _ := versions["listed"].(string)
	if token == "" {
		t.Fatalf("the list carries no version for the agent it just returned: %v", listed)
	}

	if status, body, _ := agentRequest(t, app, http.MethodPut, "/agents/listed", token,
		`{"name":"v2","enabled":true}`); status != http.StatusOK {
		t.Fatalf("the version from the list was refused by a save: %d %v", status, body)
	}
	if got := storedName(t, srv, "listed"); got != "v2" {
		t.Fatalf("the save did not take effect: name = %q", got)
	}

	// And the token is now stale, which is what makes it a version rather than
	// a decoration.
	if status, _, _ := agentRequest(t, app, http.MethodPut, "/agents/listed", token,
		`{"name":"v3","enabled":true}`); status != http.StatusConflict {
		t.Fatalf("re-using the same token after a save = %d, want 409", status)
	}
}

// The list route is not agent-mutating, so nothing else on this branch would
// notice it going quiet — and a client whose saves all start failing has no
// way to tell that the cause is a missing map on an unrelated read.
func TestEveryListedAgentCarriesAVersion(t *testing.T) {
	actor := "usr_alice"
	_, app := teamAgentApp(t, &actor)
	for _, id := range []string{"one", "two", "three"} {
		agentRequest(t, app, http.MethodPost, "/agents", "", `{"id":"`+id+`","name":"n","enabled":true}`)
	}
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	listed := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&listed)

	agents, _ := listed["agents"].([]any)
	versions, _ := listed["versions"].(map[string]any)
	if len(agents) == 0 {
		t.Fatal("no agents listed; this test would prove nothing")
	}
	for _, raw := range agents {
		def, _ := raw.(map[string]any)
		id, _ := def["id"].(string)
		if token, _ := versions[id].(string); token == "" {
			t.Errorf("agent %q is listed with no version, so it cannot be saved conditionally", id)
		}
	}
}

// The three remaining handlers that write an agent. Each had only the AST
// guard covering it, which proves a precondition call EXISTS in the source and
// nothing about what it does — a call passing the wrong `existing`, or placed
// after the write, satisfies the parser and loses work exactly as before.

// The canvas save is the write most likely to lose somebody's work: a Studio
// tab holds a whole graph in memory for as long as it stays open, so the copy
// it writes back is routinely hours old.
func TestTheStudioCanvasSaveRefusesAStaleGraph(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	if status, _, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"canvas","name":"v1","enabled":true}`); status != http.StatusCreated {
		t.Fatal("seed create failed")
	}
	_, _, stale := agentRequest(t, app, http.MethodGet, "/agents/canvas", "", "")
	if stale == "" {
		t.Fatal("no ETag to go stale")
	}

	actor = "usr_bob"
	if status, body, _ := agentRequest(t, app, http.MethodPut, "/agents/canvas", stale,
		`{"name":"bob's edit","enabled":true}`); status != http.StatusOK {
		t.Fatalf("bob's save = %d %v", status, body)
	}

	actor = "usr_alice"
	draft := `{"workflow":{"id":"canvas","name":"alice's canvas","llm":{"provider":"openai","model":"gpt-4o-mini"}}}`
	status, _, _ := agentRequest(t, app, http.MethodPost, "/studio/save", stale, draft)
	if status != http.StatusConflict {
		t.Fatalf("stale canvas save = %d, want 409", status)
	}
	if got := storedName(t, srv, "canvas"); got != "bob's edit" {
		t.Fatalf("the refused canvas save still wrote: name = %q", got)
	}
}

func TestStudioReadRoutesHandTheEditorACurrentVersion(t *testing.T) {
	actor := "usr_alice"
	_, app := teamAgentApp(t, &actor)
	if status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"studio-edit","name":"Studio edit","enabled":true,"studio_intent":"edit me"}`); status != http.StatusCreated {
		t.Fatalf("seed create = %d %v", status, body)
	}

	status, _, loadedVersion := agentRequest(t, app, http.MethodGet, "/studio/agents/studio-edit", "", "")
	if status != http.StatusOK || loadedVersion == "" {
		t.Fatalf("Studio load = %d, ETag %q; editor cannot save conditionally", status, loadedVersion)
	}

	status, listed, _ := agentRequest(t, app, http.MethodGet, "/studio/agents", "", "")
	versions, _ := listed["versions"].(map[string]any)
	listedVersion, _ := versions["studio-edit"].(string)
	if status != http.StatusOK || listedVersion == "" {
		t.Fatalf("Studio list = %d without agent version: %v", status, listed)
	}
	if listedVersion != loadedVersion {
		t.Fatalf("Studio list version %q != load ETag %q", listedVersion, loadedVersion)
	}
}

func TestStudioYAMLSaveReturnsTheNewVersion(t *testing.T) {
	actor := "usr_alice"
	_, app := teamAgentApp(t, &actor)
	if status, body, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"yaml-edit","name":"Before","enabled":false}`); status != http.StatusCreated {
		t.Fatalf("seed create = %d %v", status, body)
	}
	_, _, before := agentRequest(t, app, http.MethodGet, "/agents/yaml-edit", "", "")
	payload, _ := json.Marshal(map[string]any{"yaml": "id: yaml-edit\nname: After\nenabled: false\n"})
	status, body, written := agentRequest(t, app, http.MethodPost, "/studio/save-yaml", before, string(payload))
	if status != http.StatusOK {
		t.Fatalf("Studio YAML save = %d %v", status, body)
	}
	if written == "" || written == before {
		t.Fatalf("save returned version %q, want a new validator after %q", written, before)
	}
	_, _, current := agentRequest(t, app, http.MethodGet, "/agents/yaml-edit", "", "")
	if written != current {
		t.Fatalf("save returned %q but the stored definition is %q", written, current)
	}
}

// The Builder picks the agent ID from a natural-language description, so
// collision is MORE likely here than on a hand-typed create: two people
// describing the same job get the same name. Before the guard, a deploy
// replaced whatever was under that ID and reported success.
func TestABuilderDeployDoesNotReplaceAnExistingAgent(t *testing.T) {
	actor := "usr_alice"
	srv, app, provider := teamAgentAppWithLLM(t, &actor)
	if status, _, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"invoice-chaser","name":"alice's","enabled":true}`); status != http.StatusCreated {
		t.Fatal("seed create failed")
	}

	// Bob describes the same job to the Builder and gets the same slug, which
	// is exactly how this collides in practice: the id is derived from a
	// natural-language name, so two people describing one job produce one id.
	actor = "usr_bob"
	provider.content = `{"reply":"got it","understanding":{` +
		`"name":"Invoice Chaser","description":"chases invoices","purpose":"chase invoices",` +
		`"system_prompt":"chase invoices","confidence":0.95,"trigger":{"type":"channel"}}}`
	if status, body, _ := agentRequest(t, app, http.MethodPost, "/builder/chat", "",
		`{"session_id":"sess-bob","message":"build an invoice chaser"}`); status != http.StatusOK {
		t.Fatalf("builder chat = %d %v", status, body)
	}

	status, body, _ := agentRequest(t, app, http.MethodPost, "/builder/deploy", "",
		`{"session_id":"sess-bob"}`)
	if status != http.StatusConflict {
		t.Fatalf("builder deploy onto an existing id = %d, want 409: %v", status, body)
	}
	if got := storedName(t, srv, "invoice-chaser"); got != "alice's" {
		t.Fatalf("a builder deploy overwrote an existing agent: name = %q", got)
	}
}

// `overwrite: true` on an import answers "may this replace an agent", which is
// a different question from "replace the agent I looked at". A flag set when
// the import was composed cannot know somebody edited the agent since.
func TestAnImportWithOverwriteStillNeedsTheCurrentVersion(t *testing.T) {
	actor := "usr_alice"
	srv, app := teamAgentApp(t, &actor)
	if status, _, _ := agentRequest(t, app, http.MethodPost, "/agents", "",
		`{"id":"imported","name":"v1","enabled":true}`); status != http.StatusCreated {
		t.Fatal("seed create failed")
	}
	_, _, stale := agentRequest(t, app, http.MethodGet, "/agents/imported", "", "")

	actor = "usr_bob"
	if status, _, _ := agentRequest(t, app, http.MethodPut, "/agents/imported", stale,
		`{"name":"bob's edit","enabled":true}`); status != http.StatusOK {
		t.Fatal("bob's save failed")
	}

	actor = "usr_alice"
	// A real v2 package, because the guard sits after inspection — and it has
	// to. The request does not name the agent; the PACKAGE does, so there is
	// no earlier point at which this handler knows which agent it is about.
	// That is a genuine asymmetry with the Studio saves, and worth stating: a
	// stale import of a MALFORMED package still answers 400 first.
	pkg := `{"schema_version":"` + PackageSchemaV2 + `",` +
		`"manifest":{"agent_id":"imported","name":"packaged","trigger":"http",` +
		`"version":"2026.08.19","package_id":"acme/imported"},` +
		`"soul_yaml":"id: imported\nname: packaged\nenabled: true\ntrigger: http\nllm:\n  provider: openai\n"}`
	status, body, _ := agentRequest(t, app, http.MethodPost, "/agents/import", stale,
		`{"overwrite":true,"acknowledge_missing":true,"package":`+pkg+`}`)
	if status != http.StatusConflict {
		t.Fatalf("stale import = %d, want 409: %v", status, body)
	}
	if got := storedName(t, srv, "imported"); got != "bob's edit" {
		t.Fatalf("the refused import still wrote: name = %q", got)
	}
}
