package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func hasTool(tools []BuiltinTool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// The exemption is a single-user convenience: it gives an operator a safe
// install path without turning on shell_exec for everything.
func TestPackageInstallStaysAvailableToTheSystemAgentByDefault(t *testing.T) {
	e := &Engine{managedInstallExempt: true}
	def := &agent.Definition{ID: SystemAgentID, SystemTools: true}

	tools := e.systemToolsFor(def)
	if !hasTool(tools, "package_install") {
		t.Fatal("a single-user operator lost their only sandbox-free install path")
	}
	if hasTool(tools, "shell_exec") {
		t.Error("the exemption widened past package_install; shell access must still need " +
			"allow_system_agents")
	}
}

// Withdrawn in multi-user: the installer runs outside the sandbox and writes
// the deployment-wide config, which is an operator action.
func TestWithdrawingTheExemptionPutsItBackBehindTheServerPermit(t *testing.T) {
	e := &Engine{managedInstallExempt: false}
	def := &agent.Definition{ID: SystemAgentID, SystemTools: true}

	if hasTool(e.systemToolsFor(def), "package_install") {
		t.Fatal("a tenant's System agent can still rewrite the deployment config and install " +
			"software outside the sandbox")
	}

	// Not removed — an operator who genuinely wants it says so a second time.
	e.allowSystemAgents = []string{"*"}
	tools := e.systemToolsFor(def)
	if !hasTool(tools, "package_install") {
		t.Fatal("allow_system_agents no longer restores it, so the tool is unreachable rather " +
			"than deliberate")
	}
}

// The exemption is for the built-in System agent alone. Any other agent
// claiming the system capability must not inherit it.
func TestTheExemptionIsNotInheritedByOtherAgents(t *testing.T) {
	e := &Engine{managedInstallExempt: true}
	def := &agent.Definition{ID: "mine", SystemTools: true}
	if hasTool(e.systemToolsFor(def), "package_install") {
		t.Fatal("any agent declaring the system capability got the exemption")
	}
}
