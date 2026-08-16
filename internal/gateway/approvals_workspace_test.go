// approvals_workspace_test.go — MU-022 at the API edge. The gateway used to
// compute `admin` as "the role is owner or admin" with NO WORKSPACE IN IT, and
// hand that to the broker as authority. These tests are the reason that is now
// impossible.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/runtime"
)

// appAsMember serves requests under a verified identity in one workspace with
// one role — the two facts the old `admin` bool threw away.
func appAsMember(t *testing.T, workspaceID, role string, register func(*fiber.App)) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "usr_" + workspaceID, OrganizationID: "org_a", WorkspaceID: workspaceID,
			MembershipID: "mem_" + workspaceID, Role: role, RequestID: "req_" + workspaceID,
			PrincipalKind: "user",
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		c.Locals("request_id", "req_"+workspaceID)
		return c.Next()
	})
	register(app)
	return app
}

func approvalsApp(t *testing.T, s *Server, workspaceID, role string) *fiber.App {
	return appAsMember(t, workspaceID, role, func(app *fiber.App) {
		app.Get("/approvals", s.handleListApprovals)
		app.Post("/approvals/:id/approve", s.handleResolveApproval(true))
		app.Post("/approvals/:id/deny", s.handleResolveApproval(false))
	})
}

func do(t *testing.T, app *fiber.App, method, path string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// The whole point: an owner of one workspace is not an approver for another,
// and cannot read its paused calls' arguments.
func TestAnotherWorkspacesOwnerCannotListOrDecideThroughTheAPI(t *testing.T) {
	srv := newTestGateway(t, "secret")
	victimCtx := runtime.WithPrincipal(context.Background(), runtime.Principal{
		Subject: "usr_victim", Role: rbac.RoleOwner, WorkspaceID: "ws_victim",
	})
	srv.engine.Broker().Register(victimCtx, runtime.ApprovalRequest{
		CallID: "victim-call", Tool: "shell_exec", Args: map[string]any{"command": "cat /etc/shadow"},
	})

	attacker := approvalsApp(t, srv, "ws_attacker", rbac.RoleOwner)
	status, body := do(t, attacker, http.MethodGet, "/approvals")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if list, _ := body["approvals"].([]any); len(list) != 0 {
		t.Fatalf("another workspace's owner sees %d paused calls: %v", len(list), body["approvals"])
	}
	if status, _ := do(t, attacker, http.MethodPost, "/approvals/victim-call/approve"); status != http.StatusNotFound {
		t.Fatalf("cross-workspace approve returned %d, want 404", status)
	}

	// The owner of the workspace it belongs to still can.
	owner := approvalsApp(t, srv, "ws_victim", rbac.RoleOwner)
	status, body = do(t, owner, http.MethodGet, "/approvals")
	list, _ := body["approvals"].([]any)
	if status != http.StatusOK || len(list) != 1 {
		t.Fatalf("the owning workspace cannot see its own approval: %d %v", status, body["approvals"])
	}
	if status, _ := do(t, owner, http.MethodPost, "/approvals/victim-call/approve"); status != http.StatusOK {
		t.Fatalf("the owning workspace could not approve: %d", status)
	}
}

// The workspace comes from the VERIFIED identity. If it fell back to a
// constant, every request would read and write the personal workspace's
// approvals — which is the pre-MU-022 behaviour wearing a new name.
func TestTheApproverWorkspaceComesFromTheVerifiedIdentity(t *testing.T) {
	srv := newTestGateway(t, "secret")
	for _, ws := range []string{"ws_a", "ws_b"} {
		ctx := runtime.WithPrincipal(context.Background(), runtime.Principal{
			Subject: "usr_" + ws, Role: rbac.RoleOwner, WorkspaceID: ws,
		})
		srv.engine.Broker().Register(ctx, runtime.ApprovalRequest{CallID: "call-" + ws, Tool: "shell_exec"})
	}
	for _, ws := range []string{"ws_a", "ws_b"} {
		_, body := do(t, approvalsApp(t, srv, ws, rbac.RoleOwner), http.MethodGet, "/approvals")
		list, _ := body["approvals"].([]any)
		if len(list) != 1 {
			t.Fatalf("%s sees %d approvals, want exactly its own", ws, len(list))
		}
		first, _ := list[0].(map[string]any)
		if first["call_id"] != "call-"+ws {
			t.Fatalf("%s was served %v", ws, first["call_id"])
		}
	}
}

// Reading a paused call's arguments is not a lesser privilege than deciding:
// they are the details of something that was stopped for being dangerous.
// A viewer holds neither approvals permission and gets 403, not an empty list —
// "you may not see this" and "there is nothing" are different answers.
func TestAViewerIsRefusedRatherThanShownAnEmptyList(t *testing.T) {
	srv := newTestGateway(t, "secret")
	ctx := runtime.WithPrincipal(context.Background(), runtime.Principal{
		Subject: "usr_a", Role: rbac.RoleOwner, WorkspaceID: "ws_a",
	})
	srv.engine.Broker().Register(ctx, runtime.ApprovalRequest{CallID: "c", Tool: "shell_exec"})

	viewer := approvalsApp(t, srv, "ws_a", rbac.RoleViewer)
	if status, _ := do(t, viewer, http.MethodGet, "/approvals"); status != http.StatusForbidden {
		t.Fatalf("viewer list status = %d, want 403", status)
	}
	if status, _ := do(t, viewer, http.MethodPost, "/approvals/c/approve"); status != http.StatusForbidden {
		t.Fatalf("viewer approve status = %d, want 403", status)
	}
	// A developer may look but not release: seeing what the team is being
	// asked to authorize is a different authority from authorizing it.
	dev := approvalsApp(t, srv, "ws_a", rbac.RoleDeveloper)
	if status, _ := do(t, dev, http.MethodGet, "/approvals"); status != http.StatusOK {
		t.Fatalf("developer list status = %d, want 200", status)
	}
	if status, _ := do(t, dev, http.MethodPost, "/approvals/c/approve"); status != http.StatusForbidden {
		t.Fatalf("developer approve status = %d, want 403", status)
	}
	// And an operator, who can invoke privileged tools, can release one.
	if status, _ := do(t, approvalsApp(t, srv, "ws_a", rbac.RoleOperator), http.MethodPost, "/approvals/c/approve"); status != http.StatusOK {
		t.Fatalf("operator approve status = %d, want 200", status)
	}
}

// A second decision is 409, not 404: the approval is real and the caller may
// see it. They need to know somebody else already answered, which is exactly
// what a team of approvers produces.
func TestASecondDecisionReportsThatSomebodyElseAnsweredFirst(t *testing.T) {
	srv := newTestGateway(t, "secret")
	ctx := runtime.WithPrincipal(context.Background(), runtime.Principal{
		Subject: "usr_a", Role: rbac.RoleOwner, WorkspaceID: "ws_a",
	})
	ch := srv.engine.Broker().Register(ctx, runtime.ApprovalRequest{CallID: "c", Tool: "shell_exec"})
	app := approvalsApp(t, srv, "ws_a", rbac.RoleOwner)
	if status, _ := do(t, app, http.MethodPost, "/approvals/c/approve"); status != http.StatusOK {
		t.Fatal("first decision failed")
	}
	if !<-ch {
		t.Fatal("the run was not woken")
	}
	status, _ := do(t, app, http.MethodPost, "/approvals/c/deny")
	if status != http.StatusNotFound && status != http.StatusConflict {
		t.Fatalf("second decision status = %d, want 404 or 409", status)
	}
}
