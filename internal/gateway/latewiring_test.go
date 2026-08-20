package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
)

// THE BUG, as a test. buildApp runs inside New; SetAuth runs afterwards and
// does not rebuild. Every route registered under `if s.authEngine != nil` was
// therefore registered against a nil engine — never registered at all.
//
// Nothing 404s, which is why it hid: an unregistered /api/v1 path falls
// through to the authenticated group and answers 401. So the token exchange,
// the refresh, the logout and every OIDC route replied "invalid or missing API
// key" — the one thing you need in order to GET a credential required a
// credential.
func TestTheAuthRoutesExistWithoutACredential(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.mutateConfig(func(c *config.Config) {
		c.Deployment.Mode = config.DeploymentModeTeam
		c.Auth.Mode = "jwt"
	})
	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	// Set AFTER construction, exactly as the wiring does.
	s.SetAuth(engine)

	public := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/token"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodGet, "/api/v1/auth/oidc/config"},
	}
	for _, route := range public {
		req := httptest.NewRequest(route.method, route.path, http.NoBody)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", route.method, route.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("%s %s answered 401 without a credential — it is not registered, so it fell "+
				"through to the authenticated group. Nobody can obtain a credential",
				route.method, route.path)
		}
	}
}

// The GUI asks this endpoint whether to show the SSO button. While it 401'd,
// the button could never appear however OIDC was configured.
func TestTheOIDCDiscoveryEndpointAnswersTheLoginScreen(t *testing.T) {
	s := newTestGateway(t, "secret")
	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	s.SetAuth(engine)

	resp, err := s.app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		t.Fatalf("status = %d; the login screen reads this to decide whether SSO exists, and "+
			"treats any non-ok as 'no SSO'", resp.StatusCode)
	}
}

// Availability is a RUNTIME answer now, not a build-time one. A deployment
// with no auth engine must say so rather than 401, which reads as "your
// credential is wrong" when the truth is "there is no authentication here".
func TestWithoutAnEngineTheRoutesExplainThemselves(t *testing.T) {
	s := newTestGateway(t, "")
	if s.authEngine != nil {
		t.Fatal("setup: expected no auth engine")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", http.NoBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — the route exists and the dependency does not", resp.StatusCode)
	}
}

// The route table must not be a function of wiring ORDER. Rebuilding on each
// setter would be the same bug with a longer fuse: whichever setter ran last
// would decide what exists.
//
// Asserted against Fiber's REGISTERED ROUTES, not against a status code. An
// unregistered /api/v1 path does not 404 — it falls through to the
// authenticated group and answers 401, which is exactly how the original bug
// hid for so long. A status-code check here would reproduce that blindness:
// the first version of this test passed on a build where the RBAC routes were
// unregistered again.
func TestTheRouteTableDoesNotDependOnSetterOrder(t *testing.T) {
	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)

	wired := newTestGateway(t, "secret")
	wired.SetAuth(engine)
	bare := newTestGateway(t, "secret")

	mustExist := []string{
		"/api/v1/auth/token",
		"/api/v1/auth/refresh",
		"/api/v1/auth/logout",
		"/api/v1/auth/oidc/config",
		"/api/v1/auth/oidc/start",
		"/api/v1/auth/oidc/callback",
		"/api/v1/auth/me",
		"/api/v1/auth/reauthenticate",
		"/api/v1/rbac/policy",
		"/api/v1/rbac/grants",
	}
	for _, s := range []*Server{wired, bare} {
		registered := map[string]bool{}
		for _, route := range s.app.GetRoutes() {
			registered[route.Path] = true
		}
		for _, path := range mustExist {
			if !registered[path] {
				t.Errorf("%s is not registered; it will fall through to the authenticated group "+
					"and answer 401 rather than 404, so nothing will notice", path)
			}
		}
	}
}

// THE SECOND HALF OF THE BUG. Registering the auth routes unconditionally
// fixed OBTAINING a credential. It did not fix USING one: the authenticated
// group captured `inner := s.authHandler()` at buildApp time, when the engine
// is still nil, so every authenticated route was guarded by the legacy
// static-key middleware for the life of the process and the engine never saw a
// request.
//
// The two keys are deliberately DIFFERENT. Giving the engine the same key the
// config carries is exactly how this hid for so long: both paths accept it, so
// a passing test proves nothing about which one ran. Here only the engine
// knows "engine-key", so a 401 means the legacy middleware answered.
func TestTheAuthEngineActuallyGuardsTheAuthenticatedRoutes(t *testing.T) {
	s := newTestGateway(t, "config-key")
	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"engine-key", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	// Set AFTER construction, exactly as the wiring does.
	s.SetAuth(engine)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	req.Header.Set("Authorization", "Bearer engine-key")
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/agents with the ENGINE's key = 401; the legacy static-key "+
			"middleware answered, so the engine — and with it every JWT, OIDC and sk_ "+
			"credential this deployment issues — guards nothing (status %d)", resp.StatusCode)
	}
}

// The converse, so the test above cannot pass by the route simply being open:
// the config's key is not a credential once an engine is installed.
func TestTheConfigKeyIsNotAnAuthorityOnceTheEngineIsWired(t *testing.T) {
	s := newTestGateway(t, "config-key")
	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"engine-key", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	s.SetAuth(engine)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	req.Header.Set("Authorization", "Bearer config-key")
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/agents with the CONFIG's key = %d, want 401. The engine is the "+
			"authority; if config.Server.APIKey still admits requests, the legacy middleware "+
			"is still in the chain", resp.StatusCode)
	}
}

// The memoisation must be invalidated by SetAuth, which is why authStackCache
// is an atomic.Pointer and not a sync.Once. Serving one request before the
// engine arrives is enough to pin the legacy fallback forever otherwise — and
// health probes, the GUI's own /ping poll and any warm-up traffic all reach
// the gateway during the window New() opens and SetAuth closes.
func TestWiringTheEngineLateStillTakesEffect(t *testing.T) {
	s := newTestGateway(t, "config-key")

	// A request BEFORE the engine exists — this is what populates the cache.
	warm := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	warm.Header.Set("Authorization", "Bearer config-key")
	resp, err := s.app.Test(warm)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	engine, err := auth.New(auth.Config{Mode: "jwt", JWTSecret: "0123456789abcdef0123456789abcdef"},
		"engine-key", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	s.SetAuth(engine)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	req.Header.Set("Authorization", "Bearer engine-key")
	after, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Body.Close() }()
	if after.StatusCode == http.StatusUnauthorized {
		t.Fatalf("the engine's key = 401 after SetAuth, because one earlier request had already "+
			"memoised the legacy middleware. SetAuth must invalidate authStackCache (status %d)",
			after.StatusCode)
	}
}
