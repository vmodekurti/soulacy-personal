package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func studioScopeServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", root)
	t.Setenv("SOULACY_STUDIO_LESSONS", filepath.Join(root, "studio-lessons.db"))
	t.Setenv("SOULACY_STUDIO_MACROS", filepath.Join(root, "studio-macros.json"))
	t.Setenv("SOULACY_STUDIO_PREFERENCES", filepath.Join(root, "studio-preferences.json"))
	t.Setenv("SOULACY_STUDIO_STRATEGY_FIT", filepath.Join(root, "studio-strategy-fit.json"))
	return &Server{log: zap.NewNop(), cfg: &config.Config{}}
}

func scopeFor(t *testing.T, s *Server, workspaceID, subject string) studioScope {
	t.Helper()
	return s.studioForWorkspace(workspaceID, subject)
}

// Studio's learning is scoped by *path*, not by filtering a shared file: one
// workspace's macros are a different file, so another workspace cannot read
// them at all rather than merely being filtered out of the results.
func TestStudioLearningIsSeparateStoragePerWorkspace(t *testing.T) {
	s := studioScopeServer(t)

	a := scopeFor(t, s, "ws_a", "usr_a")
	b := scopeFor(t, s, "ws_b", "usr_b")

	macrosA, macrosB := a.macros(), b.macros()
	if macrosA == nil || macrosB == nil {
		t.Fatal("macro stores were not created")
	}
	if err := macrosA.Add(studio.WorkflowPattern{ID: "p_a", Intent: "deploy the service", Tools: []string{"shell_exec", "write_file"}}); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range macrosB.All() {
		if pattern.ID == "p_a" {
			t.Fatal("one workspace's macro was readable from another")
		}
	}
	if len(macrosA.All()) == 0 {
		t.Fatal("the owning workspace lost its own macro")
	}

	fitA, fitB := a.strategyFit(), b.strategyFit()
	if fitA == nil || fitB == nil {
		t.Fatal("strategy-fit stores were not created")
	}
	for i := 0; i < 5; i++ {
		if err := fitA.RecordProvider("openai", "gpt-x", "react", false); err != nil {
			t.Fatal(err)
		}
	}
	if len(fitA.ForProviderModel("openai", "gpt-x")) == 0 {
		t.Fatal("the owning workspace lost its own observations")
	}
	if len(fitB.ForProviderModel("openai", "gpt-x")) != 0 {
		t.Fatal("one workspace's strategy observations were readable from another")
	}
}

// Preferences are user-private inside a workspace. The store already filters by
// owner; scoping the file means one workspace's observations are not even
// present in another's storage.
func TestStudioPreferencesAreSeparatePerWorkspace(t *testing.T) {
	s := studioScopeServer(t)
	a := scopeFor(t, s, "ws_a", "usr_a")
	b := scopeFor(t, s, "ws_b", "usr_b")

	storeA, storeB := a.preferences(), b.preferences()
	if storeA == nil || storeB == nil {
		t.Fatal("preference stores were not created")
	}
	if err := storeA.MergeRulesFor("usr_a", []string{"always add a retry"}, 3); err != nil {
		t.Fatal(err)
	}
	if len(storeA.RulesFor("usr_a")) == 0 {
		t.Fatal("the owning workspace lost its own preference")
	}
	// Not visible to another workspace, even for the same-named owner.
	if len(storeB.RulesFor("usr_a")) != 0 {
		t.Fatal("one workspace's preferences were readable from another")
	}
}

// Drafts are one person's work in progress, so they are private within the
// workspace as well as across workspaces.
func TestStudioDraftsAreScopedByWorkspaceAndUser(t *testing.T) {
	base := filepath.Join(t.TempDir(), "studio", "drafts")

	seen := map[string]bool{}
	for _, tc := range []struct{ workspace, subject string }{
		{"ws_a", "usr_alice"}, {"ws_a", "usr_bob"}, {"ws_b", "usr_alice"},
	} {
		dir := wsroot.UserDir(base, tc.workspace, tc.subject)
		if seen[dir] {
			t.Fatalf("%s/%s shares a drafts directory with another principal", tc.workspace, tc.subject)
		}
		seen[dir] = true
	}
	// The personal identity keeps the plain base path, so an existing
	// installation's drafts are still where they were.
	if got := wsroot.UserDir(base, wsroot.PersonalWorkspaceID, ""); got != base {
		t.Fatalf("personal drafts moved to %q", got)
	}
}

// Product invariant 7: a Personal installation's Studio state must not move.
func TestPersonalStudioPathsAreUnchanged(t *testing.T) {
	s := studioScopeServer(t)
	personal := scopeFor(t, s, wsroot.PersonalWorkspaceID, "")

	for name, got := range map[string]string{
		"lessons":     wsroot.File(lessonsPath(), personal.WorkspaceID()),
		"macros":      wsroot.File(macrosPath(), personal.WorkspaceID()),
		"preferences": wsroot.File(preferencesPath(), personal.WorkspaceID()),
		"strategy":    wsroot.File(strategyFitPath(), personal.WorkspaceID()),
	} {
		want := map[string]string{
			"lessons": lessonsPath(), "macros": macrosPath(),
			"preferences": preferencesPath(), "strategy": strategyFitPath(),
		}[name]
		if got != want {
			t.Errorf("%s path moved for a personal install: %q want %q", name, got, want)
		}
		if strings.Contains(got, wsroot.NamespaceDir) {
			t.Errorf("%s path was namespaced for a personal install: %q", name, got)
		}
	}
}

// A scope built from a request with no verified identity is the personal
// workspace, which is what an open Personal deployment has always used.
func TestStudioScopeWithoutIdentityIsPersonal(t *testing.T) {
	s := studioScopeServer(t)
	if got := s.studio(nil).WorkspaceID(); got != wsroot.PersonalWorkspaceID {
		t.Fatalf("scope without a request = %q", got)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	var resolved studioScope
	app.Get("/x", func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "usr_alice", OrganizationID: "org_a", WorkspaceID: "ws_a",
			MembershipID: "mem_a", Role: "owner", RequestID: "req_a",
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		resolved = s.studio(c)
		return c.SendStatus(204)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resolved.WorkspaceID() != "ws_a" || resolved.Subject() != "usr_alice" {
		t.Fatalf("scope from a verified request = %+v", resolved)
	}
}

// A request scope read from a goroutine that outlives the handler is a
// use-after-free: Fiber recycles the Ctx as soon as the handler returns, and
// the failure is a nil dereference in unrelated code, far from the cause.
// Capture the scope before starting the goroutine instead.
func TestRequestScopeIsNeverReadFromADetachedGoroutine(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			statement, ok := node.(*ast.GoStmt)
			if !ok {
				return true
			}
			ast.Inspect(statement, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if selector.Sel.Name != "studio" && selector.Sel.Name != "agents" {
					return true
				}
				argument, ok := call.Args[0].(*ast.Ident)
				if !ok || argument.Name != "c" {
					return true
				}
				t.Errorf("%s:%d: s.%s(c) is read inside a goroutine — capture the scope before `go func()`, "+
					"because Fiber recycles the request context when the handler returns",
					name, fileSet.Position(inner.Pos()).Line, selector.Sel.Name)
				return true
			})
			return true
		})
	}
}
