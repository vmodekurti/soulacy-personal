//go:build loadtest

// loadprofile_test.go — MU-027's p95 targets, measured against the real
// gateway.
//
// BEHIND A BUILD TAG on purpose. This drives thousands of requests through the
// full middleware stack and takes ten seconds; running it in the ordinary
// suite would add that to every `go test ./...` and, worse, would make the
// suite's pass/fail depend on how busy the machine is. A latency gate that
// fails when somebody else's build is compiling teaches people to ignore
// failures.
//
//	make loadtest
//
// The profile it measures against is data in internal/loadprofile, with each
// target's reasoning recorded beside its number — so a change to a target is
// an argument rather than an edit.
package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/loadprofile"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/pkg/agent"
)

// loadApp mounts the measured routes behind the real workspace middleware, in
// Team mode with several workspaces — a single-tenant run would measure a code
// path no Team deployment takes, and would miss every per-workspace lookup
// this branch added.
func loadApp(t *testing.T, workspaces []string) (*Server, *fiber.App) {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam

	// Seed each workspace with enough agents that the list route does real
	// work. A list over one agent measures routing, not listing.
	for _, workspace := range workspaces {
		scope := srv.agentsForWorkspace(workspace)
		for i := 0; i < 25; i++ {
			scope.Register(&agent.Definition{
				ID:      fmt.Sprintf("agent-%02d", i),
				Name:    fmt.Sprintf("Agent %02d", i),
				Enabled: true,
			})
		}
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		workspace := c.Get("X-Load-Workspace")
		srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
			OrganizationID: "org_team", WorkspaceID: workspace, MembershipID: "mem_team", Role: tenancy.RoleOwner,
		}})
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_load"},
			Role:             tenancy.RoleOwner, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_load", AuthTime: time.Now().Unix(),
		})
		c.Locals("request_id", "req-load")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	app.Get("/agents", srv.handleListAgents)
	app.Get("/agents/:id", srv.handleGetAgent)
	app.Get("/workspace/identity", srv.handleWorkspaceIdentity)
	app.Get("/schedule", srv.handleListSchedule)
	app.Get("/health", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })
	return srv, app
}

func TestTeamPreviewMeetsItsLatencyTargets(t *testing.T) {
	profile := loadprofile.TeamPreview()
	workspaces := make([]string, 0, profile.Workspaces)
	for i := 0; i < profile.Workspaces; i++ {
		workspaces = append(workspaces, fmt.Sprintf("ws_load_%02d", i))
	}
	_, app := loadApp(t, workspaces)

	call := func(path string) loadprofile.Request {
		return func(_ context.Context, workspaceID string) error {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Load-Workspace", workspaceID)
			// The per-request timeout is generous: a timeout shorter than the
			// target would turn a slow response into an error and hide the
			// latency the measurement is about.
			resp, err := app.Test(req, 30_000)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s: status %d", path, resp.StatusCode)
			}
			return nil
		}
	}

	summaries, err := loadprofile.Run(context.Background(), loadprofile.Options{
		Profile:    profile,
		Workspaces: workspaces,
		Requests: map[loadprofile.Operation]loadprofile.Request{
			loadprofile.OpListAgents:     call("/agents"),
			loadprofile.OpGetAgent:       call("/agents/agent-07"),
			loadprofile.OpWorkspaceIdent: call("/workspace/identity"),
			loadprofile.OpListSchedule:   call("/schedule"),
			loadprofile.OpHealth:         call("/health"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	results := profile.Evaluate(summaries)
	for _, result := range results {
		t.Logf("%s target_p95=%v met=%v %s", result.Summary, result.TargetP95, result.Met, result.Note)
	}
	for _, result := range results {
		if !result.Met {
			t.Errorf("%s did not meet its target: %s", result.Operation, result.Note)
		}
	}
	if !loadprofile.Met(results) {
		t.Fatalf("the %s profile was not met", profile.Name)
	}
}
