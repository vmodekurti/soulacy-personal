package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/rbac"
)

func anyoneIn(workspaceID string, permitted ...string) approvals.Eligibility {
	allow := map[string]bool{}
	for _, p := range permitted {
		allow[p] = true
	}
	return approvals.Eligibility{
		Subject: "usr_" + workspaceID, WorkspaceID: workspaceID,
		Permits: func(resource, action string) bool { return allow[resource+":"+action] },
	}
}

func approverIn(workspaceID string) approvals.Eligibility {
	return anyoneIn(workspaceID, rbac.ResourceApprovals+":"+rbac.ActionWrite)
}

func TestBrokerRegistersListsAndResolves(t *testing.T) {
	b := newConfirmBroker()
	ctx := inWorkspace(context.Background(), "ws-a")
	ch := b.Register(ctx, ApprovalRequest{CallID: "c1", Tool: "shell_exec", Reason: "risky", AgentID: "agent-a", SessionID: "sess-1"})

	list := b.List(ctx, "ws-a")
	if len(list) != 1 || list[0].CallID != "c1" || list[0].Tool != "shell_exec" || list[0].AgentID != "agent-a" {
		t.Fatalf("metadata not captured: %+v", list)
	}
	if list[0].WorkspaceID != "ws-a" {
		t.Fatalf("approval has no workspace: %+v", list[0])
	}
	if err := b.Resolve(ctx, "ws-a", "c1", true, approverIn("ws-a"), ""); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := <-ch; !got {
		t.Fatal("expected approved=true")
	}
	if len(b.List(ctx, "ws-a")) != 0 {
		t.Fatal("pending should be empty after resolve")
	}
	if err := b.Resolve(ctx, "ws-a", "c1", true, approverIn("ws-a"), ""); err == nil {
		t.Fatal("second resolve should fail")
	}
}

func TestBrokerOnRegisterFires(t *testing.T) {
	b := newConfirmBroker()
	got := make(chan PendingApproval, 1)
	b.SetOnRegister(func(p PendingApproval) { got <- p })
	b.Register(inWorkspace(context.Background(), "ws-a"), ApprovalRequest{CallID: "x", Tool: "write_file"})
	p := <-got
	if p.CallID != "x" || p.Tool != "write_file" || p.WorkspaceID != "ws-a" {
		t.Fatalf("onRegister payload wrong: %+v", p)
	}
}

// The isolation that matters is the WORKSPACE, not the individual. MU-022
// routes an approval to *eligible approvers* — other people, by design — so
// two members of one workspace who both hold approvals:write can each answer.
// What must never happen is a member of a different workspace doing either.
//
// This replaces a per-subject check that let only the requester answer their
// own approval, which is not routing at all: it is a confirmation dialog with
// extra steps.
func TestBrokerIsolatesApprovalsByWorkspaceNotByIndividual(t *testing.T) {
	b := newConfirmBroker()
	victimCtx := inWorkspace(context.Background(), "ws-victim")
	attackerCtx := inWorkspace(context.Background(), "ws-attacker")
	ch := b.Register(victimCtx, ApprovalRequest{CallID: "victim-call", Tool: "shell_exec"})
	b.Register(attackerCtx, ApprovalRequest{CallID: "attacker-call", Tool: "write_file"})

	// A different workspace neither sees it...
	for _, p := range b.List(attackerCtx, "ws-attacker") {
		if p.CallID == "victim-call" {
			t.Fatal("another workspace's paused call is listed")
		}
	}
	// ...nor decides it, however privileged they are in their own.
	err := b.Resolve(attackerCtx, "ws-victim", "victim-call", true, approverIn("ws-attacker"), "")
	if !errors.Is(err, approvals.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// A colleague in the SAME workspace can — that is what routing means.
	colleague := approverIn("ws-victim")
	colleague.Subject = "usr_colleague"
	if err := b.Resolve(victimCtx, "ws-victim", "victim-call", true, colleague, ""); err != nil {
		t.Fatalf("an eligible colleague was refused: %v", err)
	}
	if !<-ch {
		t.Fatal("the run was not woken by the colleague's approval")
	}
}

// Holding a role is not the same as holding the permission.
func TestBrokerRefusesADeciderWithoutTheApprovalPermission(t *testing.T) {
	b := newConfirmBroker()
	ctx := inWorkspace(context.Background(), "ws-a")
	b.Register(ctx, ApprovalRequest{CallID: "c", Tool: "shell_exec"})
	chatter := anyoneIn("ws-a", rbac.ResourceChat+":"+rbac.ActionChat)
	if err := b.Resolve(ctx, "ws-a", "c", true, chatter, ""); !errors.Is(err, approvals.ErrNotEligible) {
		t.Fatalf("err = %v, want ErrNotEligible", err)
	}
}

// The arguments an approver is shown are redacted even with no store wired,
// because the in-memory path is what a personal deployment uses and the
// redaction is about what leaves the blocked goroutine, not about durability.
func TestBrokerRedactsArgumentsOnTheInMemoryPathToo(t *testing.T) {
	b := newConfirmBroker()
	ctx := inWorkspace(context.Background(), "ws-a")
	b.Register(ctx, ApprovalRequest{CallID: "c", Tool: "http_request", Args: map[string]any{
		"url": "https://api.example.com/charge", "authorization": "Bearer sk-live-REAL",
	}})
	list := b.List(ctx, "ws-a")
	if len(list) != 1 {
		t.Fatal("no approval listed")
	}
	if list[0].Args["authorization"] != "[redacted]" {
		t.Fatalf("a credential reached the approval view: %v", list[0].Args["authorization"])
	}
	if list[0].Args["url"] != "https://api.example.com/charge" {
		t.Fatalf("the field the approver judges was destroyed: %v", list[0].Args["url"])
	}
}
