package caps

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// fakeSink records audit entries in memory.
type fakeSink struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (f *fakeSink) Log(e audit.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func (f *fakeSink) all() []audit.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]audit.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}

func newEnforcer(t *testing.T, sink AuditSink) *Enforcer {
	t.Helper()
	return NewEnforcer(sink, zap.NewNop())
}

// ---------------------------------------------------------------------------
// Enforcer.Check
// ---------------------------------------------------------------------------

func TestEnforcer_Check_AllowAndAudit(t *testing.T) {
	sink := &fakeSink{}
	e := newEnforcer(t, sink)
	set := mustSet(t, "matrix", []plugin.Permission{
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
	})
	e.SetPluginSet(set)

	d := e.Check(PluginPrincipal("matrix"), CapChannelSend, "matrix")
	if !d.Allowed {
		t.Fatalf("Check denied: %s", d.Reason)
	}

	entries := sink.all()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.SessionID != "plugin:matrix" {
		t.Errorf("audit SessionID = %q, want plugin:matrix", got.SessionID)
	}
	if got.Tool != "cap:"+CapChannelSend {
		t.Errorf("audit Tool = %q, want cap:%s", got.Tool, CapChannelSend)
	}
	if got.Denied {
		t.Error("audit Denied = true, want false")
	}
	if got.Args["scope"] != "matrix" {
		t.Errorf("audit Args[scope] = %v, want matrix", got.Args["scope"])
	}
	if got.Timestamp.IsZero() {
		t.Error("audit Timestamp is zero")
	}
}

func TestEnforcer_Check_DenyAudited(t *testing.T) {
	sink := &fakeSink{}
	e := newEnforcer(t, sink)
	set := mustSet(t, "matrix", []plugin.Permission{
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
	})
	e.SetPluginSet(set)

	d := e.Check(PluginPrincipal("matrix"), CapChannelSend, "slack")
	if d.Allowed {
		t.Fatal("Check allowed unlisted scope, want deny")
	}
	entries := sink.all()
	if len(entries) != 1 || !entries[0].Denied {
		t.Fatalf("expected one denied audit entry, got %+v", entries)
	}
	if entries[0].Error == "" {
		t.Error("denied audit entry should carry the reason in Error")
	}
}

func TestEnforcer_Check_UnknownPluginDenied(t *testing.T) {
	sink := &fakeSink{}
	e := newEnforcer(t, sink)
	d := e.Check(PluginPrincipal("ghost"), CapVectorSearch, "any")
	if d.Allowed {
		t.Fatal("unknown plugin allowed, want deny")
	}
	entries := sink.all()
	if len(entries) != 1 || !entries[0].Denied {
		t.Fatalf("expected one denied audit entry, got %+v", entries)
	}
}

func TestEnforcer_Check_NonPluginPrincipalDenied(t *testing.T) {
	e := newEnforcer(t, &fakeSink{})
	d := e.Check(Principal("admin"), CapVectorSearch, "x")
	if d.Allowed {
		t.Fatal("non-plugin principal allowed via capability path, want deny")
	}
}

func TestEnforcer_NilSink_NoPanic(t *testing.T) {
	e := NewEnforcer(nil, zap.NewNop())
	d := e.Check(PluginPrincipal("p"), CapVectorSearch, "x")
	if d.Allowed {
		t.Fatal("want deny")
	}
}

func TestEnforcer_RemovePluginSet(t *testing.T) {
	e := newEnforcer(t, &fakeSink{})
	e.SetPluginSet(mustSet(t, "p1", []plugin.Permission{{Cap: CapChannelSend}}))
	if !e.Check(PluginPrincipal("p1"), CapChannelSend, "x").Allowed {
		t.Fatal("setup: expected allow")
	}
	e.RemovePluginSet("p1")
	if e.Check(PluginPrincipal("p1"), CapChannelSend, "x").Allowed {
		t.Fatal("removed plugin still allowed")
	}
}

// ---------------------------------------------------------------------------
// Fiber middleware
// ---------------------------------------------------------------------------

func testApp(e *Enforcer, claims *auth.Claims, scopeFn func(*fiber.Ctx) string) *fiber.App {
	app := fiber.New()
	app.Get("/guarded",
		func(c *fiber.Ctx) error { // simulate the auth middleware
			if claims != nil {
				auth.SetClaims(c, claims)
			}
			return c.Next()
		},
		e.RequireCapability(CapChannelSend, scopeFn),
		func(c *fiber.Ctx) error { return c.SendString("ok") },
	)
	return app
}

func pluginClaims(id string) *auth.Claims {
	return &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "plugin:" + id},
		Kind:             "access",
	}
}

func get(t *testing.T, app *fiber.App) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/guarded", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

func TestRequireCapability_NoClaims_PassesThrough(t *testing.T) {
	e := newEnforcer(t, &fakeSink{})
	resp := get(t, testApp(e, nil, nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (open mode passes)", resp.StatusCode)
	}
}

func TestRequireCapability_UserClaims_PassesThrough(t *testing.T) {
	// User principals are governed by RBAC, not capabilities.
	e := newEnforcer(t, &fakeSink{})
	cl := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		Role:             "viewer",
	}
	resp := get(t, testApp(e, cl, nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (user passes to RBAC)", resp.StatusCode)
	}
}

func TestRequireCapability_PluginAllowed(t *testing.T) {
	sink := &fakeSink{}
	e := newEnforcer(t, sink)
	e.SetPluginSet(mustSet(t, "matrix", []plugin.Permission{
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
	}))
	scopeFn := func(c *fiber.Ctx) string { return "matrix" }
	resp := get(t, testApp(e, pluginClaims("matrix"), scopeFn))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	entries := sink.all()
	if len(entries) != 1 || entries[0].Denied {
		t.Fatalf("expected one allow audit entry, got %+v", entries)
	}
}

func TestRequireCapability_PluginDenied403(t *testing.T) {
	sink := &fakeSink{}
	e := newEnforcer(t, sink)
	e.SetPluginSet(mustSet(t, "matrix", []plugin.Permission{
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
	}))
	scopeFn := func(c *fiber.Ctx) string { return "slack" }
	resp := get(t, testApp(e, pluginClaims("matrix"), scopeFn))
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	entries := sink.all()
	if len(entries) != 1 || !entries[0].Denied {
		t.Fatalf("expected one denied audit entry, got %+v", entries)
	}
}

func TestRequireCapability_UnknownPluginDenied403(t *testing.T) {
	e := newEnforcer(t, &fakeSink{})
	resp := get(t, testApp(e, pluginClaims("ghost"), nil))
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status = %d, want 403 (default-deny)", resp.StatusCode)
	}
}

func TestRequireCapability_NilScopeFn_UnscopedCheck(t *testing.T) {
	// nil scope extractor = unscoped check; allowed only when the declared
	// permission carries no scope restriction.
	e := newEnforcer(t, &fakeSink{})
	e.SetPluginSet(mustSet(t, "p1", []plugin.Permission{{Cap: CapChannelSend}}))
	resp := get(t, testApp(e, pluginClaims("p1"), nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
