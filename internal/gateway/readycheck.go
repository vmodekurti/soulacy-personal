package gateway

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
)

// readycheck.go — MU-033 criteria 3 and 4: readiness fails when a required
// shared dependency is unavailable, and a replica can be drained.
//
// WHAT /health DOES, AND WHY IT IS NOT THIS. handleHealth probes what it can
// reach and reports each dependency's state in the body — then returns 200
// whatever it found, degrading a JSON field from "ok" to "degraded". Its own
// doc comment says so: "Currently we degrade rather than returning down —
// operators can decide via the per-dep statuses returned in the body."
//
// That is a reasonable contract for a human reading a dashboard and the wrong
// one for a load balancer, which reads the status code and nothing else. A
// replica whose Postgres has gone away answers 200, stays in the pool, and
// takes its share of traffic to fail one request at a time. With one gateway
// that is merely unhelpful; with several it is the difference between losing a
// replica and losing a fraction of every user's requests.
//
// So readiness is a SEPARATE endpoint with a status code that means something,
// and /health is left exactly as it is. Changing /health's code would have
// been the smaller diff and would have broken every existing probe pointed at
// it — including the one in this repo's own container tooling.
//
// WHAT COUNTS AS REQUIRED comes from the deployment mode, and is not a second
// list. internal/config/deployment.go already decides that team and scale need
// Postgres and that scale additionally needs a durable queue, and refuses to
// start without them. Readiness asks the same question at runtime that
// startup asked at boot: a dependency the mode requires must be reachable.
// A personal deployment requires nothing shared, so its readiness is its
// liveness.

// ReadinessProbe is one dependency the gateway can check.
//
// Registered by the app rather than discovered here, because the app is what
// knows which backends were actually constructed. A gateway that inspected
// config to decide what to probe would be guessing at the wiring, and would
// report "postgres: ok" for a deployment whose postgres client failed to
// build and fell back.
type ReadinessProbe struct {
	// Name appears in the response body.
	Name string
	// Required means the deployment mode cannot function without it, so a
	// failure is a 503 rather than a note. An OPTIONAL dependency that is
	// down is reported and does not remove the replica from the pool.
	Required bool
	// Check must respect ctx and must not block past it.
	Check func(ctx context.Context) error
}

// readinessTimeout bounds the whole readiness check.
//
// A probe that hangs is indistinguishable from a dependency that is down, and
// must be treated as such: a load balancer waiting on a readiness response is
// a load balancer still sending traffic. Kept short because this endpoint is
// polled every few seconds by infrastructure, not by people.
const readinessTimeout = 2 * time.Second

// SetReadinessProbes installs the dependency checks.
func (s *Server) SetReadinessProbes(probes []ReadinessProbe) {
	if s == nil {
		return
	}
	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	s.readinessProbes = append([]ReadinessProbe(nil), probes...)
}

// BeginDraining marks this replica as leaving the pool.
//
// Readiness starts failing IMMEDIATELY, before any connection is closed. That
// ordering is the whole mechanism: the load balancer needs time to notice and
// stop routing, and every request it sends between the decision to shut down
// and that discovery is a request that will be cut off mid-flight. Closing
// first and de-registering afterwards is the common shape and it is backwards.
//
// It does not itself stop anything. Fiber's Shutdown finishes in-flight
// requests; this only ensures nothing NEW is sent here first.
func (s *Server) BeginDraining() {
	if s == nil {
		return
	}
	s.draining.Store(true)
	if s.log != nil {
		s.log.Info("gateway draining: readiness now fails, new long-lived connections refused")
	}
}

// Draining reports whether this replica is leaving the pool.
func (s *Server) Draining() bool {
	return s != nil && s.draining.Load()
}

type readinessDependency struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

// TWO ENDPOINTS, AND THE SPLIT IS ABOUT WHAT THEY DISCLOSE.
//
// A load balancer or kubelet cannot present a credential, so the probe it
// calls has to be unauthenticated — and a probe that needs the auth backend to
// answer reports "not ready" precisely when auth is the thing that broke.
// But the useful readiness BODY is a list of dependency names and driver error
// strings, and a driver error is exactly the kind of string that arrives
// carrying a DSN, a hostname, or an internal address.
//
// So: the unauthenticated endpoint returns the status CODE and one word. The
// authenticated one returns the detail. Infrastructure needs the code;
// diagnosis needs the detail and is done by somebody who has already
// authenticated. Serving both from one unauthenticated endpoint would be
// publishing the deployment's topology to anyone who can reach the port.

// handleLivePublic answers GET /ready — unauthenticated, code-only.
func (s *Server) handleLivePublic(c *fiber.Ctx) error {
	ready, label, _ := s.readinessState(c.UserContext())
	status := fiber.StatusOK
	if !ready {
		status = fiber.StatusServiceUnavailable
	}
	// No dependency list, no error strings: a driver error is a plausible
	// carrier for a DSN or an internal hostname, and this endpoint is
	// reachable by anything that can reach the port.
	return c.Status(status).JSON(fiber.Map{"status": label})
}

// handleReadinessProbe answers GET /api/v1/ready — authenticated, with detail.
//
// 200 = this replica should receive traffic. 503 = it should not, for one of
// exactly two reasons: it is draining, or a dependency its deployment mode
// requires is unreachable.
func (s *Server) handleReadinessProbe(c *fiber.Ctx) error {
	ready, label, deps := s.readinessState(c.UserContext())
	status := fiber.StatusOK
	if !ready {
		status = fiber.StatusServiceUnavailable
	}
	return c.Status(status).JSON(fiber.Map{
		"status":       label,
		"dependencies": deps,
	})
}

// readinessState runs the probes and reports whether this replica should take
// traffic. Shared by both endpoints so the two can never disagree about the
// answer while differing about how much of it they print.
func (s *Server) readinessState(parent context.Context) (bool, string, []readinessDependency) {
	if s.Draining() {
		// Answered before the probes run. A draining replica is not ready
		// whatever its dependencies say, and probing them would delay the
		// answer the load balancer is waiting for by up to readinessTimeout.
		return false, "draining", nil
	}

	s.readinessMu.RLock()
	probes := append([]ReadinessProbe(nil), s.readinessProbes...)
	s.readinessMu.RUnlock()

	ctx, cancel := context.WithTimeout(parent, readinessTimeout)
	defer cancel()

	results := make([]readinessDependency, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		results[i] = readinessDependency{Name: probe.Name, Required: probe.Required}
		if probe.Check == nil {
			// A probe with no check is a claim with nothing behind it, and
			// silently reporting "ok" for it is worse than having no probe:
			// it makes the endpoint say the dependency was verified.
			results[i].Status = "unprobeable"
			results[i].Detail = "no check registered"
			continue
		}
		wg.Add(1)
		// Concurrently: the timeout bounds the whole check, so sequential
		// probes would make N slow dependencies take N × the budget and the
		// last one would never be reached.
		go func(i int, probe ReadinessProbe) {
			defer wg.Done()
			if err := probe.Check(ctx); err != nil {
				results[i].Status = "error"
				results[i].Detail = err.Error()
				return
			}
			results[i].Status = "ok"
		}(i, probe)
	}
	wg.Wait()

	ready := true
	for _, dep := range results {
		// FAIL CLOSED on "unprobeable" as well as on "error", for REQUIRED
		// dependencies only. "We could not tell" and "it is fine" are
		// different answers, and a readiness endpoint that conflates them
		// reports healthy for exactly the misconfiguration it exists to catch.
		if dep.Required && dep.Status != "ok" {
			ready = false
		}
	}
	sort.Slice(results, func(a, b int) bool { return results[a].Name < results[b].Name })

	if !ready {
		return false, "not_ready", results
	}
	return true, "ready", results
}
