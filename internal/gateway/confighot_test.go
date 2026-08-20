// confighot_test.go — the reload path must apply what the catalog promises.
//
// internal/confighot says which sections are live. This says ReloadConfig
// actually applies them. Without it the catalog would be a wish: a section
// could be classified live, with a plausible AppliedBy naming a real function,
// and nothing on the reload path calling it — which is precisely the state
// this whole change started from, where thirty sections were editable and one
// was applied.
//
// A source guard rather than a behaviour test, because exercising ReloadConfig
// for real needs a config file, a live engine, a rate limiter, a channel
// applier and an LLM router, and a test that stubs all of those proves the
// stubs were called. What must be true is that the reload path MENTIONS every
// live section's applier, and that is a property of the source.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/confighot"
)

// reloadBody returns config.go's source. Used by the guards below that check
// ORDER, which is a textual property.
func reloadBody(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if !strings.Contains(body, "func (s *Server) ReloadConfig()") {
		t.Fatal("ReloadConfig is not in config.go; this guard now vouches for nothing")
	}
	return body
}

// reloadCalls returns every function name REACHABLE from ReloadConfig by
// following calls into helpers defined in the same file.
//
// Two weaker implementations were tried and both let a real regression through.
// A substring search over the file matched `func (s *Server) applyLLMLive(`,
// so an applier DEFINED here passed whether or not anything called it. Then
// collecting every call site anywhere in the file matched
// `s.engine.SetSearchConfig(...)` inside applySearchLive, so deleting
// applySearchLive's call from ReloadConfig still passed — the applier had
// become dead code and the catalog still claimed the section was live.
//
// Reachability is the property the catalog actually asserts, so it is the
// property checked. Cross-file helpers are not followed: every applier lives
// beside ReloadConfig, and following imports would turn this into a
// whole-program analysis for no extra coverage.
func reloadCalls(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	local := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			local[fn.Name.Name] = fn
		}
	}
	root, ok := local["ReloadConfig"]
	if !ok {
		t.Fatal("ReloadConfig is not in config.go; this guard now vouches for nothing")
	}

	called := map[string]bool{}
	var walk func(fn *ast.FuncDecl)
	walk = func(fn *ast.FuncDecl) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch target := call.Fun.(type) {
			case *ast.Ident:
				name = target.Name
			case *ast.SelectorExpr:
				name = target.Sel.Name
			}
			if name == "" || called[name] {
				return true
			}
			called[name] = true
			if next, isLocal := local[name]; isLocal {
				walk(next)
			}
			return true
		})
	}
	walk(root)
	return called
}

func TestReloadConfigAppliesEverySectionTheCatalogCallsLive(t *testing.T) {
	called := reloadCalls(t)

	// Sections whose "live" claim is a per-request read of the config snapshot
	// rather than a pushed value. Those need nothing on the reload path —
	// publishing the new snapshot IS the application — and demanding a call for
	// them would push somebody to write a no-op just to satisfy the guard.
	perRequestRead := regexp.MustCompile(`(?i)read |stamped `)

	for _, section := range confighot.Sections {
		if section.Apply != confighot.ApplyLive {
			continue
		}
		for _, claimed := range section.AppliedBy {
			if perRequestRead.MatchString(claimed) {
				continue
			}
			symbol := claimed
			if i := strings.LastIndex(symbol, "."); i >= 0 {
				symbol = symbol[i+1:]
			}
			if !called[symbol] {
				t.Errorf("confighot classifies %q as live, applied by %q, and the reload path never calls "+
					"it. A section that is editable and not applied is worse than one that says it needs a "+
					"restart: the operator pays a cost they do not know about",
					section.Field, claimed)
			}
		}
	}
}

func TestTheReloadPathRestoresVaultBackedSecrets(t *testing.T) {
	// secrets.Migrate blanks vault-backed values in memory as well as on disk
	// and relies on Overlay to restore them. ReloadConfig calls config.Load,
	// which re-reads the blanked file — so a reload without an Overlay empties
	// every provider key and channel token in the running config.
	//
	// Nothing breaks functionally, which is why it survived: the router and
	// the started adapters hold constructed clients. What breaks is the truth
	// of every surface that reads the config, including the doctor telling
	// operators to re-save keys that are safely in the vault.
	body := reloadBody(t)
	if !strings.Contains(body, "s.secrets.Overlay(") {
		t.Error("ReloadConfig does not re-run the secrets overlay; every config write will blank the " +
			"vault-backed values in the in-memory config")
	}
	overlay := strings.Index(body, "s.secrets.Overlay(")
	swap := strings.Index(body, "s.setConfig(newCfg)")
	if overlay < 0 || swap < 0 || overlay > swap {
		t.Error("the overlay must run on the freshly loaded config BEFORE it is installed, or the " +
			"gateway serves the blanked version for the window in between")
	}
}

func TestNoHandlerStillTellsOperatorsToRestartForALiveSection(t *testing.T) {
	// The stale-message half. A dozen places told operators to restart after
	// saving a provider key, which had been hot for months, and the channel
	// handlers said the same thing while genuinely doing nothing. Both are
	// wrong in ways that cost the operator something: one wastes a restart,
	// the other hides that a restart is needed.
	//
	// Scoped to this package's handler responses. The advisory text in
	// internal/llm's provider doctor is checked by its own package.
	restartAdvice := regexp.MustCompile(`(?i)restart the gateway (to|for|so|after)`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for i, line := range strings.Split(string(source), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !restartAdvice.MatchString(line) {
				continue
			}
			// Advice about a section that genuinely IS boot-only is correct
			// and must stay.
			if mentionsBootOnlySection(line) {
				continue
			}
			t.Errorf("%s:%d tells an operator to restart the gateway, and names no boot-only config "+
				"section. If the setting is live the advice is stale; if it is not, classify it in "+
				"internal/confighot so the claim is checkable:\n\t%s", name, i+1, trimmed)
		}
	}
}

func mentionsBootOnlySection(line string) bool {
	lowered := strings.ToLower(line)
	for _, section := range confighot.BootOnly() {
		if strings.Contains(lowered, section.Key) {
			return true
		}
	}
	return false
}
