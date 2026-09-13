package gateway

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/rbac"
	"go.uber.org/zap"
)

type reportActivityStub struct {
	fakeTailBackend
	start, end time.Time
	err        error
	budget     time.Duration
}

func (r *reportActivityStub) OperationsActivity(ctx context.Context, start, end time.Time) (actionlog.OperationsActivity, error) {
	r.start, r.end = start, end
	if deadline, ok := ctx.Deadline(); ok {
		r.budget = time.Until(deadline)
	}
	return actionlog.OperationsActivity{ByAgent: []actionlog.AgentActivity{}}, r.err
}

func TestOperationsReportRespectsConfiguredHTTPTimeout(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Runtime.Timeouts.HTTP = "50ms"
	backend := &reportActivityStub{}
	s.actions = backend
	status, _ := gatewayJSON(t, s, "GET", "/api/v1/reports/operations", "secret", "")
	if status != 200 || backend.budget <= 0 || backend.budget > 50*time.Millisecond {
		t.Fatalf("report budget = %v, status = %d", backend.budget, status)
	}
}

func TestOperationsReportAvailabilityAndWindow(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, "GET", "/api/v1/reports/operations", "secret", "")
	if status != 200 || body["status"] != "unavailable" || body["activity"] != nil || body["usage"] != nil {
		t.Fatalf("missing = %d %v", status, body)
	}
	backend := &reportActivityStub{}
	s.actions = backend
	cs, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	s.SetCostStore(cs)
	for _, window := range []string{"24h", "7d", "30d"} {
		status, body = gatewayJSON(t, s, "GET", "/api/v1/reports/operations?window="+window, "secret", "")
		if status != 200 || body["status"] != "ready" {
			t.Fatalf("ready = %d %v", status, body)
		}
		want := map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}[window]
		if backend.end.Sub(backend.start) != want || backend.end.Nanosecond() != 0 {
			t.Fatalf("bounds = %v %v", backend.start, backend.end)
		}
	}
	backend.err = fmt.Errorf("private database path /secret and token")
	status, body = gatewayJSON(t, s, "GET", "/api/v1/reports/operations", "secret", "")
	if status != 200 || body["status"] != "partial" || body["activity"] != nil {
		t.Fatalf("partial = %d %v", status, body)
	}
	if strings.Contains(fmt.Sprint(body["sources"]), "/secret") {
		t.Fatal("raw error leaked")
	}
	_ = cs.Close()
	status, body = gatewayJSON(t, s, "GET", "/api/v1/reports/operations", "secret", "")
	if status != 200 || body["status"] != "unavailable" || body["usage"] != nil {
		t.Fatalf("error = %d %v", status, body)
	}
}

func TestOperationsReportHTTPGuards(t *testing.T) {
	s := newTestGateway(t, "secret")
	for _, tc := range []struct {
		method, path, key string
		want              int
	}{
		{"GET", "/api/v1/reports/operations", "", 401},
		{"GET", "/api/v1/reports/operations?window=all", "secret", 400},
		{"GET", "/api/v1/reports/operations?window=-24h", "secret", 400},
		{"GET", "/api/v1/reports/operations?window=999999d", "secret", 400},
		{"POST", "/api/v1/reports/operations", "secret", 405},
	} {
		status, _ := gatewayJSON(t, s, tc.method, tc.path, tc.key, "")
		if status != tc.want {
			t.Errorf("%s %s = %d want %d", tc.method, tc.path, status, tc.want)
		}
	}
	req := mustRequest(t, http.MethodGet, "/api/v1/reports/operations")
	req.Header.Set("Authorization", "Bearer secret")
	res, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("report can be cached")
	}
	s = newTestGateway(t, "")
	if status, _ := gatewayJSON(t, s, "GET", "/api/v1/reports/operations", "", ""); status != 503 {
		t.Fatalf("open mode = %d", status)
	}
}

func TestOperationsReportMetricsAuthorization(t *testing.T) {
	for _, tc := range []struct {
		role   string
		scopes []string
		want   int
	}{
		{rbac.RoleAdmin, nil, 200}, {rbac.RoleOperator, nil, 403}, {rbac.RoleViewer, nil, 403},
		{rbac.RoleAdmin, []string{"agents:read"}, 403}, {rbac.RoleAdmin, []string{"metrics:read"}, 200},
	} {
		s := newTestGateway(t, "secret")
		s.SetRBAC(rbac.NewManager(rbac.NoopStore{}, zap.NewNop()))
		app := fiber.New()
		app.Use(func(c *fiber.Ctx) error {
			auth.SetClaims(c, &auth.Claims{Role: tc.role, Scopes: tc.scopes, Kind: "access"})
			return c.Next()
		})
		app.Get("/report", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleOperationsReport)
		res, err := app.Test(httptestutil.WithHost(mustRequest(t, "GET", "/report")))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("%s %v = %d want %d", tc.role, tc.scopes, res.StatusCode, tc.want)
		}
	}
}
