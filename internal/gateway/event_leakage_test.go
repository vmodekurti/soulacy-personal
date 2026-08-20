// event_leakage_test.go — MU-026 criterion 7: cross-user and cross-workspace
// leakage, including the two kinds the criterion names explicitly — sessionless
// events and administrative events.
package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

func teamServer(owners map[string]sessionOwner) *Server {
	return withCfg(&Server{
		sessionOwners: owners,
	}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
}

func subscriber(workspaceID, principal, role string, admin bool) eventPrincipal {
	return eventPrincipal{
		Principal: principal, WorkspaceID: workspaceID, Role: role,
		Admin: admin, Authenticated: true,
	}
}

// THE gap. With no session there is no owner record, so tenancy rested on an
// RBAC call asking whether the SUBSCRIBER's workspace lets their role read an
// agent with that ID — never whether the EVENT belonged to that workspace.
// Agent IDs are unique per workspace, so two tenants with a "support-bot" both
// satisfied it.
func TestASessionlessEventDoesNotCrossWorkspaces(t *testing.T) {
	s := teamServer(nil)
	event := message.Event{Type: "tool.call", AgentID: "support-bot", WorkspaceID: "ws_victim"}

	if s.authorizeEvent(subscriber("ws_attacker", "operator:mallory", "operator", false), event) {
		t.Fatal("a sessionless event reached another workspace's subscriber")
	}
	// And an admin of the wrong workspace is still the wrong workspace — the
	// administrative case the criterion names.
	if s.authorizeEvent(subscriber("ws_attacker", "admin:mallory", "admin", true), event) {
		t.Fatal("a sessionless event reached another workspace's admin")
	}
}

// An unstamped sessionless event has nothing establishing its tenant at all,
// so it is refused for any named workspace rather than authorized by an agent
// ID that means something different in each of them.
func TestAnUnstampedSessionlessEventReachesNoNamedWorkspace(t *testing.T) {
	s := teamServer(nil)
	event := message.Event{Type: "tool.call", AgentID: "support-bot"}

	if s.authorizeEvent(subscriber("ws_a", "operator:alice", "operator", false), event) {
		t.Fatal("an event with no workspace was served to a named workspace")
	}
	if s.authorizeEvent(subscriber("ws_a", "admin:alice", "admin", true), event) {
		t.Fatal("an event with no workspace was served to a named workspace's admin")
	}
	// Personal keeps receiving it, which is what a single-tenant deployment
	// has always seen (invariant 7).
	if !s.authorizeEvent(subscriber(wsroot.PersonalWorkspaceID, "operator:local", "operator", false), event) {
		t.Fatal("the personal workspace stopped receiving its own sessionless events")
	}
}

// A stamped event may never cross, whatever the session owner says. This is
// strictly stronger than owner-based authorization and covers the case where
// an owner record is stale or wrong.
func TestAStampedEventNeverCrossesEvenWithAMatchingOwner(t *testing.T) {
	// The owner record claims ws_attacker owns the session — the event's own
	// stamp says otherwise, and the stamp wins.
	s := teamServer(map[string]sessionOwner{
		"run-1": {Principal: "operator:mallory", WorkspaceID: "ws_attacker", AgentID: "bot"},
	})
	event := message.Event{Type: "tool.result", AgentID: "bot", SessionID: "run-1", WorkspaceID: "ws_victim"}

	if s.authorizeEvent(subscriber("ws_attacker", "operator:mallory", "operator", false), event) {
		t.Fatal("a stamped event was released on the strength of an owner record from another workspace")
	}
}

// The owner still decides within the right workspace, so the boundary is a
// boundary rather than a ban.
func TestTheSessionOwnerStillDecidesInsideTheRightWorkspace(t *testing.T) {
	s := teamServer(map[string]sessionOwner{
		"run-1": {Principal: "operator:alice", WorkspaceID: "ws_a", AgentID: "bot"},
	})
	event := message.Event{Type: "tool.result", AgentID: "bot", SessionID: "run-1", WorkspaceID: "ws_a"}

	if !s.authorizeEvent(subscriber("ws_a", "operator:alice", "operator", false), event) {
		t.Fatal("the session owner was denied their own event")
	}
	// A colleague in the same workspace who does not own the session is still
	// refused: workspace scoping does not replace per-principal ownership.
	if s.authorizeEvent(subscriber("ws_a", "operator:bob", "operator", false), event) {
		t.Fatal("a session's events were visible to a non-owner in the same workspace")
	}
}

// An unauthenticated subscriber receives nothing, stamped or not.
func TestAnUnauthenticatedSubscriberReceivesNothing(t *testing.T) {
	s := teamServer(nil)
	for _, event := range []message.Event{
		{Type: "tool.call", AgentID: "bot", WorkspaceID: "ws_a"},
		{Type: "tool.call", AgentID: "bot"},
		{Type: "run.status", SessionID: "run-1", WorkspaceID: "ws_a"},
	} {
		if s.authorizeEvent(eventPrincipal{}, event) {
			t.Fatalf("an unauthenticated subscriber received %s", event.Type)
		}
	}
}

// The personal-mode admin shortcut must stay shut in Team and Scale, or every
// other check in this function is decoration.
func TestThePersonalAdminShortcutIsClosedInMultiUserMode(t *testing.T) {
	personal := withCfg(&Server{}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModePersonal}})
	team := teamServer(nil)
	event := message.Event{Type: "tool.call", AgentID: "bot", WorkspaceID: "ws_victim"}
	admin := subscriber("ws_attacker", "admin:mallory", "admin", true)

	if !personal.authorizeEvent(admin, event) {
		t.Fatal("personal mode lost its admin shortcut")
	}
	if team.authorizeEvent(admin, event) {
		t.Fatal("the personal admin shortcut is open in team mode")
	}
}
