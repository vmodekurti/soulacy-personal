package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The behaviour tests in internal/runtime and internal/config both pass on a
// build where boot never applies the policy — and that build is the original
// state, where the System agent was reachable by every workspace member in
// every mode.
//
// Order matters too: LoadAll is where a SOUL.yaml claiming the reserved ID
// gets promoted to a system-tools agent, so the switch has to be thrown first.
func TestBootDisablesPlatformAgentsBeforeItLoadsAnyFromDisk(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire_subsystems.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var setPos, loadPos token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "SetPlatformAgentsEnabled":
			if setPos == token.NoPos {
				setPos = call.Pos()
			}
		case "LoadAll":
			if loadPos == token.NoPos {
				loadPos = call.Pos()
			}
		}
		return true
	})
	if setPos == token.NoPos {
		t.Fatal("boot never applies the platform-agent policy; the System agent is reachable by " +
			"every workspace member in every deployment mode")
	}
	if loadPos == token.NoPos {
		t.Fatal("wire_subsystems.go no longer calls LoadAll; this guard vouches for nothing")
	}
	if setPos > loadPos {
		t.Fatal("the policy is applied after LoadAll, so a SOUL.yaml claiming the reserved ID is " +
			"promoted to a system-tools agent before anything switches it off")
	}
}

// The policy is one decision. A second place deriving it from the mode is how
// two answers to the same question appear, and only one of them gets fixed.
func TestOnlyTheConfigPolicyDecidesWhetherPlatformAgentsExist(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	// config/deployment.go owns the rule; the tests that pin it name the
	// constant too, and a doctor check is allowed to REPORT the decision.
	allowed := map[string]bool{
		filepath.Join("internal", "config", "deployment.go"):          true,
		filepath.Join("internal", "config", "platformagents_test.go"): true,
		filepath.Join("internal", "app", "wire_subsystems.go"):        true,
		filepath.Join("internal", "app", "platformagents_test.go"):    true,
		filepath.Join("cmd", "sy", "doctor.go"):                       true,
	}
	var offenders []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || allowed[rel] {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(body), "UnsafeTenantSystemAgentAcknowledgement") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if len(offenders) > 0 {
		t.Errorf("the System-agent acknowledgement is consulted outside the one place that owns "+
			"the policy: %s. Take the answer from config.PlatformAgentsEnabled instead, or add the "+
			"file here with a reason", strings.Join(offenders, ", "))
	}
}

// The behaviour is tested in internal/runtime; that boot applies it is not.
func TestBootWithdrawsTheInstallExemptionInMultiUser(t *testing.T) {
	body, err := os.ReadFile("wire_subsystems.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "SetManagedInstallExempt(false)") {
		t.Fatal("boot never withdraws the package_install exemption, so a tenant's System agent " +
			"can install software outside the sandbox and rewrite the deployment config")
	}
	// Guarded by the mode, not applied unconditionally: a personal
	// installation losing its only sandbox-free install path is a regression.
	index := strings.Index(source, "SetManagedInstallExempt(false)")
	window := source[max(0, index-400):index]
	if !strings.Contains(window, "IsMultiUserMode") {
		t.Error("the exemption is withdrawn without checking the deployment mode; personal " +
			"installations lose their install path")
	}
}
