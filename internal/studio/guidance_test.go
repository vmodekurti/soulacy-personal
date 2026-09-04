package studio

// Guidance style: a remedy has to be readable by the person who has to act on it.
//
// Every check in this package ends in a sentence a user reads when something is
// wrong. Those sentences were written by people who already knew where every
// setting lived, so they named the setting and stopped: "Set LLM.response_format:
// json_object", "Narrow with policy.shell: prompt", "Insert an LLM Extract or
// Python Transform node". Each is correct. None of them tells someone who has
// not read this codebase what to do, or where.
//
// The rule this enforces is deliberately narrow, because a test cannot judge
// prose: IF a remedy names an internal thing — a config key, an internal type,
// a piece of product vocabulary — THEN it must also say where that thing lives,
// or that Studio can do it for you. Naming `max_turns` is fine. Naming it
// without "in the SOUL.yaml view" is not.
//
// This is not a substitute for writing well. It is a floor: it catches the
// specific failure that made the Save step unusable — a remedy that reads like
// a note between two engineers.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// internalVocabulary is language that means nothing to someone who has not read
// the source: config keys, Go field names, and product terms we invented.
var internalVocabulary = []string{
	// config keys and struct fields
	"NewAgents", "allowed_providers", "policy.shell", "deny_paths", "confirm_tools",
	"max_turns", "response_format", "output_schema", "total_timeout", "step_timeout",
	"run_timeout", "intent_gate", "accept_privileged_exposure", "Builtin", "MCPTool",
	// vocabulary we invented
	"completion contract", "typed ports", "LLM Extract", "Python Transform",
	"ReAct", "macro-workflow", "Non-Negotiables", "allowlist", "guardrail",
	"free-form", "structured tool", "Auto reasoning agent", "output route", "predicate",
	// template syntax
	"toJson",
}

// groundingPhrases are the ways a remedy can say WHERE. One of these has to
// appear alongside any internal vocabulary.
var groundingPhrases = []string{
	"inspector", "soul.yaml", "delivery page", "providers page", "channels & delivery",
	"settings", "</> tab", "canvas", "build step", "save step", "mcp page", "secrets page",
	"palette on the left", "toolbar", "the button", "studio can", "on the right",
	"this step", "step's",
}

// remedyStrings pulls every user-facing fix sentence out of the package: the
// last argument of add()/addFix(), and any `Fix:` field in a struct literal.
// Parsed rather than grepped so comments and unrelated strings cannot sneak in
// (an earlier regex version of this reported 56 violations, most of them prose
// out of doc comments).
func remedyStrings(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if kv, ok := n.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Fix" {
					if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil && strings.TrimSpace(s) != "" {
							out[s] = f
						}
					}
				}
			}
			if ce, ok := n.(*ast.CallExpr); ok {
				name := ""
				if id, ok := ce.Fun.(*ast.Ident); ok {
					name = id.Name
				}
				if (name == "add" || name == "addFix") && len(ce.Args) >= 6 {
					if lit, ok := ce.Args[5].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil && strings.TrimSpace(s) != "" {
							out[s] = f
						}
					}
				}
			}
			return true
		})
	}
	return out
}

func TestRemediesDoNotSpeakInInternalVocabulary(t *testing.T) {
	remedies := remedyStrings(t)
	if len(remedies) < 20 {
		t.Fatalf("only found %d remedy strings — the extractor has stopped matching", len(remedies))
	}
	for fix, file := range remedies {
		low := strings.ToLower(fix)
		var jargon []string
		for _, j := range internalVocabulary {
			if strings.Contains(low, strings.ToLower(j)) {
				jargon = append(jargon, j)
			}
		}
		if len(jargon) == 0 {
			continue
		}
		grounded := false
		for _, p := range groundingPhrases {
			if strings.Contains(low, p) {
				grounded = true
				break
			}
		}
		if !grounded {
			t.Errorf("%s: this remedy names %v but never says where to find it, so only someone who has read the source can act on it:\n    %s",
				file, jargon, fix)
		}
	}
}

// A remedy that fits in a few words is almost always the "note between two
// engineers" shape — it names a setting and trusts you to know the rest.
func TestRemediesExplainRatherThanGesture(t *testing.T) {
	for fix, file := range remedyStrings(t) {
		if n := len(strings.Fields(fix)); n < 6 {
			t.Errorf("%s: this remedy is %d words, too short to say what to do and where:\n    %s", file, n, fix)
		}
	}
}
