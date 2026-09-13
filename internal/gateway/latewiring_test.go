package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/httptestutil"
)

func lateWiredTestEngine(t *testing.T, key string) *auth.Engine {
	t.Helper()
	e, err := auth.New(auth.Config{
		Mode: "jwt", JWTSecret: strings.Repeat("test-only-", 4),
	}, key, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestAuthRoutesAreRegisteredBeforeEngineWiring(t *testing.T) {
	s := newTestGateway(t, "config-key")
	s.SetAuth(lateWiredTestEngine(t, "engine-key"))

	registered := map[string]bool{}
	for _, route := range s.app.GetRoutes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /api/v1/auth/token",
		"POST /api/v1/auth/refresh",
		"GET /api/v1/auth/me",
	} {
		if !registered[route] {
			t.Errorf("%s is not registered", route)
		}
	}
}

func TestLateWiredLoginCreatesUsableBrowserSession(t *testing.T) {
	s := newTestGateway(t, "config-key")
	s.SetAuth(lateWiredTestEngine(t, "engine-key"))

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token",
		strings.NewReader(`{"api_key":"engine-key"}`))
	login.Header.Set("Content-Type", "application/json")
	resp, err := s.app.Test(httptestutil.WithHost(login))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	resp.Body.Close()
	if len(cookies) < 2 {
		t.Fatalf("login set %d cookies, want access and refresh cookies", len(cookies))
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	resp, err = s.app.Test(httptestutil.WithHost(request))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("the session cookie issued by login was rejected by the authenticated API group")
	}
}

func TestLateWiredEngineGuardsAuthenticatedRoutes(t *testing.T) {
	s := newTestGateway(t, "config-key")
	s.SetAuth(lateWiredTestEngine(t, "engine-key"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	req.Header.Set("Authorization", "Bearer engine-key")
	resp, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("the build-time legacy middleware handled a credential owned by the late-wired engine")
	}
}

func TestSetAuthInvalidatesWarmupMiddleware(t *testing.T) {
	s := newTestGateway(t, "config-key")
	warm := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	warm.Header.Set("Authorization", "Bearer config-key")
	resp, err := s.app.Test(httptestutil.WithHost(warm))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	s.SetAuth(lateWiredTestEngine(t, "engine-key"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", http.NoBody)
	req.Header.Set("Authorization", "Bearer engine-key")
	resp, err = s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("SetAuth did not replace the authentication middleware cached by warm-up traffic")
	}
}
