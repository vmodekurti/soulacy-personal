package studio

// Coverage: no finding may be a dead end.
//
// A blocker or warning that says what is wrong and nothing about what to do
// next is where a user stops. So every one of them must carry three things:
// the message, a written fix that names what to change and where, and a
// machine-readable action the UI can render as a button.
//
// The action is not always Studio doing the work. There are three honest tiers:
//
//   apply    — Studio owns the value and just sets it.
//   focus    — Studio cannot decide it, but can put the exact step or control
//              in front of you (reveal the node, open the model picker).
//   navigate — the setting lives on another screen that owns it.
//
// What is NOT allowed is a finding with no action at all. This test walks a
// corpus of deliberately broken drafts through the whole pipeline and fails if
// any blocker or warning reaches the user without one.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// brokenDrafts covers one draft per failure family the contract can report.
func brokenDrafts() map[string]Draft {
	manyNodes := func() []sdkr.FlowNode {
		var ns []sdkr.FlowNode
		for i := 0; i < 9; i++ {
			ns = append(ns, sdkr.FlowNode{ID: fmt.Sprintf("n%d", i), Kind: "python", Code: "def run(i):\n    return i", Output: fmt.Sprintf("o%d", i)})
		}
		return ns
	}
	longPrompt := "You are a helper with a prompt long enough to clear the eighteen word floor this codebase applies to peer agents everywhere."

	return map[string]Draft{
		"no delivery route": {
			Name: "Digest", Intent: "every morning summarise the news and send it to me on telegram",
			Channels: []string{"http"},
			Flow:     Flow{Entry: "s", Nodes: []sdkr.FlowNode{{ID: "s", Kind: "tool", Tool: "web_search", Input: `{"query":"n"}`, Output: "r"}}},
		},
		"thin helper prompt": {
			Name: "T", Channels: []string{"http"},
			NewAgents: []NewAgent{{ID: "summarizer", SystemPrompt: "Sum."}},
			Flow:      Flow{Entry: "a", Nodes: []sdkr.FlowNode{{ID: "a", Kind: "agent", Agent: "summarizer", Output: "r"}}},
		},
		"free-form handoff into a typed tool": {
			Name: "F", Channels: []string{"telegram"},
			NewAgents: []NewAgent{{ID: "sum", SystemPrompt: longPrompt}},
			Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "agent", Agent: "sum", Output: "text"},
				{ID: "b", Kind: "tool", Tool: "kb_write", Input: "{{ .text }}", Output: "w"},
			}, Edges: []sdkr.FlowEdge{{From: "a", To: "b"}}},
		},
		"too many steps, unreachable nodes": {
			Name: "N", Channels: []string{"telegram"},
			Flow: Flow{Entry: "n0", Nodes: manyNodes()},
		},
		"privileged tools on a shared channel": {
			Name: "P", Channels: []string{"telegram"}, Tools: []string{"shell_exec", "web_search"},
			Flow: Flow{Entry: "s", Nodes: []sdkr.FlowNode{{ID: "s", Kind: "tool", Tool: "shell_exec", Input: `{"command":"ls"}`, Output: "r"}}},
		},
	}
}

func TestEveryBlockerAndWarningIsActionable(t *testing.T) {
	for name, draft := range brokenDrafts() {
		t.Run(name, func(t *testing.T) {
			report := Readiness(ReadinessInput{
				Draft:      draft,
				Catalog:    Catalog{},
				Definition: &agent.Definition{ID: "x", Capabilities: []string{"system"}},
			})

			seen := 0
			for _, group := range [][]ReadinessItem{report.Blockers, report.Warnings} {
				for _, item := range group {
					if item.Severity != "block" && item.Severity != "warn" {
						continue
					}
					seen++
					where := fmt.Sprintf("%s/%s %q", item.Section, item.Kind, clip(item.Message, 60))
					if strings.TrimSpace(item.Fix) == "" {
						t.Errorf("%s has no written fix — the user is told what is wrong and nothing else", where)
					}
					if strings.TrimSpace(item.Action) == "" {
						t.Errorf("%s has no action, so it renders as text with no way forward", where)
					}
					if strings.TrimSpace(item.ActionLabel) == "" {
						t.Errorf("%s has an action but no button text, so nothing renders", where)
					}
					if !IsFixAction(item.Action) && item.Action != "" {
						t.Errorf("%s carries action %q, which is outside the vocabulary", where, item.Action)
					}
				}
			}
			if seen == 0 {
				t.Fatalf("this draft was supposed to produce findings; the corpus has gone stale")
			}
		})
	}
}

// The written fix has to say something a user can act on. A remedy that only
// restates the problem ("this workflow has a problem") is the failure mode this
// guards: it satisfies "non-empty" while helping nobody.
func TestEveryFixNamesSomethingToDo(t *testing.T) {
	// A fix should either name a place, or use an imperative verb.
	verbs := []string{"add", "set", "open", "tick", "pick", "choose", "give", "insert", "combine",
		"collapse", "remove", "replace", "restrict", "switch", "wrap", "fix", "pass", "review",
		"studio can", "the button", "audit", "connect", "configure", "drop", "write", "edit", "provide", "feed"}
	for name, draft := range brokenDrafts() {
		for _, c := range AssessContract(draft, Catalog{}, PreflightInput{}).Checks {
			if c.Status == "pass" || c.Fix == "" {
				continue
			}
			low := strings.ToLower(c.Fix)
			ok := false
			for _, v := range verbs {
				if strings.Contains(low, v) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("[%s] %s: the fix does not tell the user to DO anything: %q", name, c.ID, c.Fix)
			}
		}
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
