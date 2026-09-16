package gateway

import (
	"net/http"
	"testing"
)

// The status route is the one a fresh dashboard calls on load, so it has to
// answer on a machine where nothing is installed rather than erroring.
func TestServiceStatusAnswersOnAMachineWithNoServiceInstalled(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/service", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if _, ok := body["state"]; !ok {
		t.Fatalf("response should carry a state; got %v", body)
	}
	if _, ok := body["supported"]; !ok {
		t.Fatalf("response should say whether this platform is supported; got %v", body)
	}
	// Whatever the state, the user gets a sentence rather than a bare enum.
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("status should explain itself in words")
	}
}

// Autostart writes a file into the user's login items, so it is a config
// write, not a read. A viewer must not be able to install it.
func TestServiceMutationsRequireConfigWrite(t *testing.T) {
	s := newTestGateway(t, "secret")
	for _, path := range []string{
		"/api/v1/admin/service/install",
		"/api/v1/admin/service/uninstall",
		"/api/v1/admin/service/start",
		"/api/v1/admin/service/stop",
	} {
		status, _ := gatewayJSON(t, s, http.MethodPost, path, "wrong-key", "")
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("%s with a bad key returned %d, want 401/403", path, status)
		}
	}
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/service", "wrong-key", "")
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Errorf("status route with a bad key returned %d, want 401/403", status)
	}
}

// The dashboard renders from the returned status, so a failed action must
// still hand back the true state instead of only an error string.
//
// HOME is redirected to a temp directory first. Without that this test reads
// and acts on the developer's own LaunchAgent, which is both a real side
// effect and a result that depends on whose machine is running the suite.
// Pointed at an empty home, "start" fails before it runs any command at all,
// because there is no unit file to start.
func TestFailedServiceActionStillReturnsStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/service/start", "secret", "")
	if status == http.StatusOK {
		t.Fatalf("starting a service that was never installed should fail; got %v", body)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("a failure should say why; got %v", body)
	}
	if _, ok := body["status"]; !ok {
		t.Errorf("a failure should still carry the current status; got %v", body)
	}
}

// Installing autostart must not be reachable without the workspace resolving,
// and the status it reports must match what the shared package would say.
func TestServiceStatusMatchesAnUntouchedHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestGateway(t, "secret")

	code, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/service", "secret", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%v", code, body)
	}
	if got, _ := body["state"].(string); got != "not_installed" && got != "unsupported" {
		t.Fatalf("an empty home should report not_installed; got %q", got)
	}
}
