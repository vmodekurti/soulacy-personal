package runtime

import (
	"testing"
)

func TestConfirmBroker_RegisterListResolve(t *testing.T) {
	b := newConfirmBroker()
	ch := b.RegisterRequest(ConfirmRequest{CallID: "c1", Tool: "shell_exec", Reason: "risky"}, "agent-a", "sess-1")

	list := b.List()
	if len(list) != 1 {
		t.Fatalf("pending = %d, want 1", len(list))
	}
	if list[0].CallID != "c1" || list[0].Tool != "shell_exec" || list[0].AgentID != "agent-a" {
		t.Fatalf("metadata not captured: %+v", list[0])
	}

	if !b.Resolve("c1", true) {
		t.Fatalf("resolve should succeed")
	}
	if got := <-ch; got != true {
		t.Fatalf("expected approved=true")
	}
	if len(b.List()) != 0 {
		t.Fatalf("pending should be empty after resolve")
	}
	if b.Resolve("c1", true) {
		t.Fatalf("second resolve should fail")
	}
}

func TestConfirmBroker_OnRegisterFires(t *testing.T) {
	b := newConfirmBroker()
	got := make(chan PendingApproval, 1)
	b.SetOnRegister(func(p PendingApproval) { got <- p })
	b.RegisterRequest(ConfirmRequest{CallID: "x", Tool: "write_file"}, "a", "s")
	p := <-got
	if p.CallID != "x" || p.Tool != "write_file" {
		t.Fatalf("onRegister payload wrong: %+v", p)
	}
}

func TestConfirmBroker_BackCompatRegister(t *testing.T) {
	b := newConfirmBroker()
	ch := b.Register("legacy")
	if len(b.List()) != 1 {
		t.Fatalf("legacy Register should still appear in List")
	}
	b.Resolve("legacy", false)
	if <-ch != false {
		t.Fatalf("expected denied")
	}
}

func TestConfirmBroker_PrincipalIsolation(t *testing.T) {
	b := newConfirmBroker()
	alice := b.RegisterRequestForPrincipal(ConfirmRequest{CallID: "alice-call", Tool: "write_file"}, "agent", "s-alice", "viewer:alice")
	b.RegisterRequestForPrincipal(ConfirmRequest{CallID: "bob-call", Tool: "shell_exec"}, "agent", "s-bob", "viewer:bob")

	list := b.ListForPrincipal("viewer:alice", false)
	if len(list) != 1 || list[0].CallID != "alice-call" {
		t.Fatalf("alice approvals = %+v", list)
	}
	if b.ResolveForPrincipal("bob-call", true, "viewer:alice", false) {
		t.Fatal("cross-principal approval resolution succeeded")
	}
	if !b.ResolveForPrincipal("alice-call", false, "viewer:alice", false) || <-alice {
		t.Fatal("owner could not deny their approval")
	}
}
