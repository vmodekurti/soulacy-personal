// workspace_test.go — a capability grant belongs to one workspace, and dies
// with the plugin that earned it.
//
// Two bugs, both silent, both in the direction of MORE access:
//
//   - The grant map was keyed by plugin ID alone. Plugin IDs are chosen by
//     whoever wrote the plugin, so two workspaces installing different plugins
//     under the same ID meant the second install decided what the FIRST one
//     was allowed to do.
//   - RemovePluginSet existed and had no callers. Revoking a plugin removed
//     its tools and left its capability grant standing, so a plugin revoked
//     for misbehaving kept every host API its manifest had asked for until
//     the gateway restarted. The tools disappear visibly, which is what makes
//     it look complete.
package caps

import (
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/plugin"
)

func grantingEnforcer(t *testing.T) *Enforcer {
	t.Helper()
	return NewEnforcer(nil, zap.NewNop())
}

// grantSet builds a set allowing one capability for one plugin. Uses the
// package's existing mustSet helper so these tests and the older ones cannot
// disagree about what a valid grant looks like.
func grantSet(t *testing.T, pluginID, capability string) *Set {
	t.Helper()
	return mustSet(t, pluginID, []plugin.Permission{{Cap: capability}})
}

func TestOneWorkspacesGrantDoesNotApplyInAnother(t *testing.T) {
	e := grantingEnforcer(t)
	// The same plugin ID in two workspaces, asking for different things. This
	// is not contrived: plugin IDs are author-chosen, so a collision needs no
	// malice at all.
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))

	principal := PluginPrincipal("acme")
	if d := e.Check("ws-a", principal, CapEventsSubscribe, ""); !d.Allowed {
		t.Fatal("the workspace that granted the capability was denied it")
	}
	if d := e.Check("ws-b", principal, CapEventsSubscribe, ""); d.Allowed {
		t.Error("a workspace that never installed this plugin inherited its capability — a grant is " +
			"only worth something inside the workspace that approved it")
	}
}

func TestASecondWorkspacesInstallDoesNotReplaceTheFirstsGrant(t *testing.T) {
	// The original shape of the bug: last install wins, for everybody.
	e := grantingEnforcer(t)
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))
	e.SetPluginSet("ws-b", grantSet(t, "acme", CapVectorSearch))

	principal := PluginPrincipal("acme")
	if d := e.Check("ws-a", principal, CapEventsSubscribe, ""); !d.Allowed {
		t.Error("ws-a lost its capability when ws-b installed a plugin with the same id")
	}
	if d := e.Check("ws-a", principal, CapVectorSearch, ""); d.Allowed {
		t.Error("ws-a gained a capability it never granted, from another workspace's install")
	}
}

func TestRevokingAPluginTakesItsCapabilityWithIt(t *testing.T) {
	e := grantingEnforcer(t)
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))

	e.RemovePluginSet("ws-a", "acme")

	if d := e.Check("ws-a", PluginPrincipal("acme"), CapEventsSubscribe, ""); d.Allowed {
		t.Error("a revoked plugin kept its capability grant — its tools are gone, so the revocation " +
			"looks complete while it can still reach the host API it asked for")
	}
}

func TestRemovingAPluginInOneWorkspaceLeavesTheOther(t *testing.T) {
	e := grantingEnforcer(t)
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))
	e.SetPluginSet("ws-b", grantSet(t, "acme", CapEventsSubscribe))

	e.RemovePluginSet("ws-a", "acme")

	if d := e.Check("ws-b", PluginPrincipal("acme"), CapEventsSubscribe, ""); !d.Allowed {
		t.Error("one workspace's revocation removed another workspace's grant")
	}
}

func TestReconcilingReplacesAWorkspacesGrantsExactly(t *testing.T) {
	// Reconciliation has to handle all three lifecycle events, and the one
	// that is easy to miss is the third: a plugin re-approved with a manifest
	// asking for MORE than before. A remove-only path leaves the old narrow
	// grant; an add-only path leaves the old broad one.
	e := grantingEnforcer(t)
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))
	e.SetPluginSet("ws-a", grantSet(t, "beta", CapVectorSearch))

	// acme's manifest changed and beta was revoked.
	e.ReplaceWorkspaceSets("ws-a", []*Set{grantSet(t, "acme", CapVectorSearch)})

	if d := e.Check("ws-a", PluginPrincipal("acme"), CapVectorSearch, ""); !d.Allowed {
		t.Error("a re-approved plugin did not receive its new capability")
	}
	if d := e.Check("ws-a", PluginPrincipal("acme"), CapEventsSubscribe, ""); d.Allowed {
		t.Error("a re-approved plugin kept a capability its new manifest no longer asks for")
	}
	if d := e.Check("ws-a", PluginPrincipal("beta"), CapVectorSearch, ""); d.Allowed {
		t.Error("a plugin absent from the reconciled set kept its grant")
	}
}

func TestReconcilingOneWorkspaceLeavesAnotherAlone(t *testing.T) {
	e := grantingEnforcer(t)
	e.SetPluginSet("ws-a", grantSet(t, "acme", CapEventsSubscribe))
	e.SetPluginSet("ws-b", grantSet(t, "acme", CapEventsSubscribe))

	e.ReplaceWorkspaceSets("ws-a", nil)

	if d := e.Check("ws-b", PluginPrincipal("acme"), CapEventsSubscribe, ""); !d.Allowed {
		t.Error("reconciling one workspace to empty cleared another workspace's grants")
	}
}
