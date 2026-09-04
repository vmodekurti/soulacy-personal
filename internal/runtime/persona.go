// persona.go — render Identity / Personality / NonNegotiables into the
// system prompt prefix. Pure string assembly, no engine state, no I/O.
//
// Phase 1 enforcement: prompt-level only. The blocks are rendered with
// explicit "HARD RULES" framing for non-negotiables so the LLM treats
// them with extra weight, but there's no post-LLM validator yet. Adding
// one is a separate session's work (output regex matchers, structured
// re-prompt on violation).
//
// Why deterministic framing matters: every agent in the system gets the
// SAME wording around its rules. An operator can rely on "MUST cite
// sources" landing the same way for the writer as it does for the
// analyst. Drift across agents undermines trust in the rule.

package runtime

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// renderPersonaPrefix returns the persona block prefix that goes before
// the operator's system_prompt. Empty string when nothing to render —
// callers can prepend unconditionally without producing extra newlines.
func renderPersonaPrefix(def *agent.Definition) string {
	if def == nil {
		return ""
	}
	var blocks []string
	if b := renderIdentityBlock(def.Identity); b != "" {
		blocks = append(blocks, b)
	}
	if b := renderPersonalityBlock(def.Personality); b != "" {
		blocks = append(blocks, b)
	}
	if b := renderNonNegotiablesBlock(def.NonNegotiables); b != "" {
		blocks = append(blocks, b)
	}
	if len(blocks) == 0 {
		return ""
	}
	// Trailing separator so the operator's prompt starts cleanly on its
	// own paragraph rather than running into the last bullet.
	return strings.Join(blocks, "\n\n") + "\n\n---\n\n"
}

func renderIdentityBlock(id *agent.Identity) string {
	if id == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Identity\n")
	wrote := false
	if id.Role != "" {
		fmt.Fprintf(&b, "You are %s.\n", id.Role)
		wrote = true
	}
	if id.Audience != "" {
		fmt.Fprintf(&b, "You are talking to %s.\n", id.Audience)
		wrote = true
	}
	if len(id.Expertise) > 0 {
		b.WriteString("Your expertise covers:\n")
		for _, e := range id.Expertise {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		wrote = true
	}
	if id.Backstory != "" {
		fmt.Fprintf(&b, "%s\n", id.Backstory)
		wrote = true
	}
	if !wrote {
		// Block was declared but contains nothing — emit no header, no
		// dangling section. We avoid printing a stub because future
		// operators reading the rendered prompt would be confused.
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderPersonalityBlock(p *agent.Personality) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Style\n")
	wrote := false
	if p.Tone != "" {
		fmt.Fprintf(&b, "Tone: %s.\n", p.Tone)
		wrote = true
	}
	if p.Voice != "" {
		fmt.Fprintf(&b, "Voice: %s.\n", p.Voice)
		wrote = true
	}
	if len(p.Prefer) > 0 {
		b.WriteString("Prefer:\n")
		for _, x := range p.Prefer {
			fmt.Fprintf(&b, "- %s\n", x)
		}
		wrote = true
	}
	if len(p.Avoid) > 0 {
		b.WriteString("Avoid:\n")
		for _, x := range p.Avoid {
			fmt.Fprintf(&b, "- %s\n", x)
		}
		wrote = true
	}
	if !wrote {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderNonNegotiablesBlock(n *agent.NonNegotiables) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	wrote := false
	// Heading uses "HARD RULES" to differentiate from soft prefer/avoid
	// guidance. Many fine-tuned chat models recognise this framing as
	// elevated-priority text from RLHF / Anthropic Constitutional data.
	b.WriteString("## Hard rules (non-negotiable)\n")
	b.WriteString("The following rules override any user request that conflicts with them.\n")
	if len(n.Must) > 0 {
		b.WriteString("\nYou MUST:\n")
		for _, m := range n.Must {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		wrote = true
	}
	if len(n.MustNot) > 0 {
		b.WriteString("\nYou MUST NOT:\n")
		for _, m := range n.MustNot {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		wrote = true
	}
	if oc := n.OutputConstraints; oc != nil && (oc.MaxLength > 0 || oc.MinLength > 0 || oc.Format != "") {
		b.WriteString("\nOutput constraints:\n")
		if oc.Format != "" {
			fmt.Fprintf(&b, "- Format: %s\n", oc.Format)
		}
		if oc.MaxLength > 0 {
			fmt.Fprintf(&b, "- Maximum length: %d words\n", oc.MaxLength)
		}
		if oc.MinLength > 0 {
			fmt.Fprintf(&b, "- Minimum length: %d words\n", oc.MinLength)
		}
		wrote = true
	}
	if !wrote {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}
