// readycheck_test.go — readiness has to answer with a STATUS CODE.
//
// Every assertion here is on the code, not the body, because the code is the
// only part a load balancer reads. /health already reports dependency failures
// accurately in its body and returns 200 regardless, which is why a replica
// with a dead database stayed in the pool: the information was present and
// nothing acted on it.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

func readyServer(t *testing.T, probes []ReadinessProbe) (*Server, *fiber.App) {
	t.Helper()
	s := &Server{log: zap.NewNop()}
	s.SetReadinessProbes(probes)
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Get("/ready", s.handleLivePublic)
	app.Get("/api/v1/ready", s.handleReadinessProbe)
	return s, app
}

func getStatus(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestARequiredDependencyBeingDownTakesTheReplicaOutOfThePool(t *testing.T) {
	_, app := readyServer(t, []ReadinessProbe{{
		Name:     "postgres",
		Required: true,
		Check:    func(context.Context) error { return errors.New("connection refused") },
	}})

	status, _ := getStatus(t, app, "/ready")
	if status != http.StatusServiceUnavailable {
		t.Errorf("readiness returned %d with its required database down — a load balancer reads the "+
			"code and would keep routing here", status)
	}
}

func TestAnOptionalDependencyBeingDownDoesNot(t *testing.T) {
	// The distinction is the whole reason Required exists. Removing a replica
	// from the pool because an optional subsystem is degraded converts a
	// partial outage into a total one, which is how readiness endpoints
	// acquire a reputation for making incidents worse.
	_, app := readyServer(t, []ReadinessProbe{{
		Name:     "knowledge",
		Required: false,
		Check:    func(context.Context) error { return errors.New("index rebuilding") },
	}})

	status, _ := getStatus(t, app, "/ready")
	if status != http.StatusOK {
		t.Errorf("readiness returned %d for a degraded OPTIONAL dependency", status)
	}
}

func TestARequiredDependencyWithNoCheckIsNotReady(t *testing.T) {
	// "We could not tell" is not "it is fine". A team deployment whose
	// Postgres client never got built registers a probe with no Check, and
	// reporting that as ready would make the misconfiguration this endpoint
	// exists to catch look exactly like health.
	_, app := readyServer(t, []ReadinessProbe{{Name: "postgres", Required: true}})

	status, body := getStatus(t, app, "/api/v1/ready")
	if status != http.StatusServiceUnavailable {
		t.Errorf("an unprobeable REQUIRED dependency reported ready (%d)", status)
	}
	deps, _ := body["dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("expected the dependency to be reported, got %v", body["dependencies"])
	}
	first, _ := deps[0].(map[string]any)
	if first["status"] != "unprobeable" {
		t.Errorf("status = %v, want \"unprobeable\" — reporting it as an error would blame the "+
			"dependency for a wiring mistake", first["status"])
	}
}

func TestDrainingIsReportedBeforeTheProbesRun(t *testing.T) {
	probed := false
	s, app := readyServer(t, []ReadinessProbe{{
		Name:     "postgres",
		Required: true,
		Check:    func(context.Context) error { probed = true; return nil },
	}})

	s.BeginDraining()
	status, body := getStatus(t, app, "/ready")

	if status != http.StatusServiceUnavailable {
		t.Errorf("a draining replica reported ready (%d)", status)
	}
	if body["status"] != "draining" {
		t.Errorf("status = %v, want \"draining\" — an operator watching a rolling restart needs to "+
			"tell a planned drain from a broken dependency", body["status"])
	}
	// Not merely an optimisation: the probes have a two-second budget, and a
	// draining replica making the balancer wait for it is a draining replica
	// still receiving traffic for two more seconds.
	if probed {
		t.Error("a draining replica still waited on its dependency probes before answering")
	}
}

func TestThePublicProbeDisclosesNoDependencyDetail(t *testing.T) {
	// A driver error is a plausible carrier for a DSN, a hostname, or an
	// internal address, and /ready is reachable by anything that can reach
	// the port.
	_, app := readyServer(t, []ReadinessProbe{{
		Name:     "postgres",
		Required: true,
		Check: func(context.Context) error {
			return errors.New(`failed to connect to host=db-primary.internal user=soulacy`)
		},
	}})

	status, body := getStatus(t, app, "/ready")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if _, present := body["dependencies"]; present {
		t.Error("the unauthenticated probe returned a dependency list")
	}
	raw, _ := json.Marshal(body)
	if string(raw) == "" || strings.Contains(string(raw), "db-primary.internal") {
		t.Errorf("the unauthenticated probe leaked deployment topology: %s", raw)
	}

	// And the authenticated one must still carry it, or an operator has no
	// way to find out why the replica is out of the pool.
	_, authed := getStatus(t, app, "/api/v1/ready")
	authedRaw, _ := json.Marshal(authed)
	if !strings.Contains(string(authedRaw), "db-primary.internal") {
		t.Errorf("the authenticated probe dropped the detail too, leaving nothing to diagnose: %s", authedRaw)
	}
}

func TestDrainingRefusesANewWebSocketButKeepsServingHTTP(t *testing.T) {
	// The two halves of criterion 4, and they pull in opposite directions:
	// stop taking NEW long-lived connections, while allowing bounded
	// completion of what is already running. A drain that also rejected
	// ordinary requests would cut off the in-flight work it is supposed to
	// let finish.
	s := &Server{log: zap.NewNop()}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use("/ws", func(c *fiber.Ctx) error {
		if s.Draining() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "draining"})
		}
		return c.SendString("upgrade would proceed")
	})
	app.Get("/api/v1/agents", func(c *fiber.Ctx) error { return c.SendString("ok") })

	s.BeginDraining()

	if status, _ := getStatus(t, app, "/ws/events"); status != http.StatusServiceUnavailable {
		t.Errorf("a draining replica accepted a new WebSocket (%d) — it would be torn down again "+
			"in seconds, and the client would have reconnected to a healthy replica if refused", status)
	}
	if status, _ := getStatus(t, app, "/api/v1/agents"); status != http.StatusOK {
		t.Errorf("a draining replica refused an ordinary request (%d), cutting off the in-flight "+
			"work the grace period exists to let finish", status)
	}
}

func TestTheDrainHappensBeforeTheListenerCloses(t *testing.T) {
	s := &Server{log: zap.NewNop()}

	var order []string
	drainingWhenStopped := false
	slept := time.Duration(0)

	s.drainThenStop(
		func(d time.Duration) {
			// Recorded DURING the wait: the grace period only does anything
			// if readiness is already failing while it elapses. A build that
			// drained after sleeping would still sleep, still shut down
			// cleanly, and give the load balancer no window at all.
			slept = d
			order = append(order, "sleep")
			if !s.Draining() {
				t.Error("the grace period elapsed while this replica was still reporting ready — " +
					"the load balancer had nothing to notice, so the wait bought nothing")
			}
		},
		func() error {
			order = append(order, "stop")
			drainingWhenStopped = s.Draining()
			return nil
		},
	)

	if !drainingWhenStopped {
		t.Error("the listener closed before readiness started failing — every request the load " +
			"balancer sent in between arrived at a socket that was already going away")
	}
	if len(order) != 2 || order[0] != "sleep" || order[1] != "stop" {
		t.Errorf("order = %v, want the grace period before the shutdown", order)
	}
	if slept != drainGracePeriod {
		t.Errorf("waited %v, want the full grace period %v", slept, drainGracePeriod)
	}
}

func TestShutdownIsBounded(t *testing.T) {
	// Fiber's Shutdown waits FOREVER for in-flight requests. One streaming
	// agent run — the normal case here — therefore blocks process exit until
	// somebody sends SIGKILL, which is the ungraceful shutdown the graceful
	// path existed to avoid. Source-level because the bound is an argument to
	// a Fiber call, and calling it for real would need a live listener.
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if !strings.Contains(body, "ShutdownWithTimeout(shutdownTimeout)") {
		t.Error("the bounded shutdown call is gone")
	}
	if strings.Contains(body, "s.app.Shutdown()") {
		t.Error("an unbounded s.app.Shutdown() is back; it waits forever for in-flight requests")
	}
}
