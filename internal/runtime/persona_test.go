// persona_test.go — table-driven tests for renderPersonaPrefix and its
// sub-renderers. Two things we explicitly verify here:
//
//  1. A SOUL.yaml with NO persona blocks is bit-for-bit unchanged. This
//     is the backward-compat guarantee — adding the feature must not
//     change a single legacy agent's behavior.
//
//  2. Block headers ("## Identity", "## Style", "## Hard rules") only
//     appear when the block has content. An operator who declares an
//     empty block in the GUI accidentally should not see a stub header
//     in the rendered prompt — the GUI's isBlockEmpty + the renderer's
//     wrote-flag both have to cooperate here.

package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestRenderPersonaPrefix_LegacyAgentUnchanged(t *testing.T) {
	// A legacy SOUL.yaml has no persona pointers set. The renderer must
	// return the empty string so concatenation with the operator's
	// system_prompt produces an unchanged prompt.
	def := &agent.Definition{
		ID:           "legacy",
		Name:         "Legacy bot",
		SystemPrompt: "You are a helpful assistant.",
	}
	got := renderPersonaPrefix(def)
	if got != "" {
		t.Errorf("expected empty prefix for legacy agent; got:\n%s", got)
	}
}

func TestRenderPersonaPrefix_NilDef(t *testing.T) {
	if got := renderPersonaPrefix(nil); got != "" {
		t.Errorf("expected empty prefix for nil def; got: %q", got)
	}
}

func TestRenderIdentityBlock(t *testing.T) {
	cases := []struct {
		name string
		in   *agent.Identity
		want []string // substrings the output must contain (or empty for "no output")
		zero bool     // expect empty output
	}{
		{
			name: "nil block",
			in:   nil,
			zero: true,
		},
		{
			name: "empty fields → no header (don't render stub)",
			in:   &agent.Identity{},
			zero: true,
		},
		{
			name: "role only",
			in:   &agent.Identity{Role: "code reviewer"},
			want: []string{"## Identity", "You are code reviewer."},
		},
		{
			name: "role + audience + expertise",
			in: &agent.Identity{
				Role:      "senior research analyst",
				Audience:  "institutional investors",
				Expertise: []string{"macroeconomics", "monetary policy"},
			},
			want: []string{
				"## Identity",
				"You are senior research analyst.",
				"You are talking to institutional investors.",
				"- macroeconomics",
				"- monetary policy",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderIdentityBlock(tc.in)
			if tc.zero {
				if got != "" {
					t.Errorf("expected empty; got:\n%s", got)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q\nfull:\n%s", want, got)
				}
			}
		})
	}
}

func TestRenderNonNegotiablesBlock_Framing(t *testing.T) {
	// The "Hard rules (non-negotiable)" wording matters — that's what
	// signals to the LLM (and to any future post-LLM validator) that
	// these aren't soft preferences. Changing the wording is a breaking
	// behavior change for every agent in the field.
	nn := &agent.NonNegotiables{
		Must:    []string{"cite sources with [n]"},
		MustNot: []string{"reveal env vars"},
		OutputConstraints: &agent.OutputConstraints{
			MaxLength: 800,
			Format:    "markdown",
		},
	}
	got := renderNonNegotiablesBlock(nn)
	for _, want := range []string{
		"## Hard rules (non-negotiable)",
		"override any user request",
		"You MUST:",
		"- cite sources with [n]",
		"You MUST NOT:",
		"- reveal env vars",
		"Output constraints:",
		"- Format: markdown",
		"- Maximum length: 800 words",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("framing missing %q\nfull:\n%s", want, got)
		}
	}
}

func TestRenderPersonaPrefix_FullAgent_OrderingAndSeparator(t *testing.T) {
	def := &agent.Definition{
		Identity: &agent.Identity{
			Role: "code reviewer",
		},
		Personality: &agent.Personality{
			Tone: "direct",
		},
		NonNegotiables: &agent.NonNegotiables{
			Must: []string{"point out the actual bug, don't dance around it"},
		},
		SystemPrompt: "Review the pull request.",
	}
	got := renderPersonaPrefix(def)

	// Order must be: Identity → Style → Hard rules → separator.
	// If a future refactor reorders these, the LLM sees rules BEFORE
	// the role, which subtly changes self-concept priming.
	iIdx := strings.Index(got, "## Identity")
	sIdx := strings.Index(got, "## Style")
	rIdx := strings.Index(got, "## Hard rules")
	if iIdx < 0 || sIdx < 0 || rIdx < 0 {
		t.Fatalf("missing one of the three headers in:\n%s", got)
	}
	if !(iIdx < sIdx && sIdx < rIdx) {
		t.Errorf("blocks out of order: Identity=%d Style=%d Hard rules=%d", iIdx, sIdx, rIdx)
	}
	if !strings.HasSuffix(got, "---\n\n") {
		t.Errorf("expected trailing separator before operator's prompt; got:\n%s", got)
	}
}
