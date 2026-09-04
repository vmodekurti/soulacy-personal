package studio

import (
	"context"
	"fmt"
	"strings"
)

// GenerationProfile is the explicit contract Studio uses to make local builder
// models behave more like stronger cloud models. It records which scaffolds were
// active for a compile and whether the resulting build should be verified before
// trusting it.
type GenerationProfile struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Local          bool   `json:"local"`
	Compact        bool   `json:"compact,omitempty"`
	Strong         bool   `json:"strong,omitempty"`
	StrictMode     bool   `json:"strict_mode,omitempty"`
	PlanMatched    bool   `json:"plan_matched,omitempty"`
	PatternMatched bool   `json:"pattern_matched,omitempty"`
	LessonsApplied int    `json:"lessons_applied,omitempty"`
	Confidence     string `json:"confidence,omitempty"`  // high | medium | low
	NextAction     string `json:"next_action,omitempty"` // save | build_verify | ask_clarify | use_frontier
}

// BuildGenerationProfile derives the generation profile from the builder model
// and the current intent/catalog. The gateway fills provider/model/locality from
// config; this package adds pattern/plan context.
func BuildGenerationProfile(provider, model, baseURL, intent string, cat Catalog) GenerationProfile {
	gp := GenerationProfile{
		Provider: provider,
		Model:    model,
		Local:    IsLocalProvider(provider, baseURL),
		Compact:  isSmallModel(model) && !isStrongModel(model),
		Strong:   isStrongModel(model),
	}
	gp.StrictMode = gp.Local || gp.Compact
	gp.PlanMatched = len(BuildPlan(intent, cat)) > 0
	gp.PatternMatched = len(MatchPatterns(intent, cat, 1)) > 0
	gp.LessonsApplied = len(cat.Lessons)
	gp.Confidence, gp.NextAction = generationConfidence(gp)
	return gp
}

func generationConfidence(gp GenerationProfile) (confidence, nextAction string) {
	if !gp.Local && gp.Strong {
		return "high", "save"
	}
	if gp.PlanMatched || gp.PatternMatched {
		if gp.Compact {
			return "medium", "build_verify"
		}
		return "high", "save"
	}
	if gp.Compact {
		return "low", "ask_clarify"
	}
	if gp.Local {
		return "medium", "build_verify"
	}
	return "medium", "build_verify"
}

// GenerationProfilePromptBlock turns the profile into concise instructions for
// the model. This is intentionally operational: the LLM gets a checklist it can
// follow, while deterministic validators still enforce correctness afterwards.
func GenerationProfilePromptBlock(gp *GenerationProfile) string {
	if gp == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nSTUDIO BUILDER PROFILE — follow this mode exactly:\n")
	if gp.Local {
		sb.WriteString("- Builder locality: LOCAL. Prefer deterministic, schema-safe workflows that can be validated offline.\n")
	} else {
		sb.WriteString("- Builder locality: CLOUD. Still obey all local validation and privacy-safe workflow rules.\n")
	}
	if gp.Compact {
		sb.WriteString("- Compact local model contract: do NOT invent architecture. Use the provided proven pattern / deterministic plan when present. Keep the graph small, literal, and JSON-only.\n")
		sb.WriteString("- Before returning, mentally run a compiler pass: every node kind is valid, every referenced variable is produced upstream, every tool input is valid JSON, and every output path ends at end.\n")
		sb.WriteString("- If uncertain, choose a simpler fixed workflow or an auto agent; do not create elaborate branching, shell commands, or implicit state.\n")
	} else if gp.Local {
		sb.WriteString("- Local model contract: use proven patterns, strict tool contracts, and simple JSON handoffs. Prefer fewer reliable nodes over clever graphs.\n")
	}
	if gp.PlanMatched {
		sb.WriteString("- A deterministic plan is available below. Realise it exactly; only fill concrete tools, args, and prompts.\n")
	} else if gp.PatternMatched {
		sb.WriteString("- A proven pattern is available below. Follow its step ordering and data-flow contract.\n")
	}
	if gp.LessonsApplied > 0 {
		sb.WriteString("- Apply the lessons from past runs; they came from real failures in this workspace.\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func shouldRepairMalformedDraft(gp *GenerationProfile) bool {
	return gp != nil && (gp.Local || gp.Compact || gp.StrictMode)
}

func repairMalformedDraft(ctx context.Context, llm LLM, originalPrompt, raw string, parseErr error) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("studio: no llm for malformed draft repair")
	}
	excerpt := strings.TrimSpace(raw)
	if len(excerpt) > 6000 {
		excerpt = excerpt[:6000] + "\n...[truncated]"
	}
	var sb strings.Builder
	sb.WriteString("You are repairing a Soulacy Studio draft JSON response.\n")
	sb.WriteString("The previous response failed to parse. Return ONLY one valid JSON object matching the Studio draft schema from the original instruction. No prose, no markdown, no code fences.\n\n")
	sb.WriteString("Parse error:\n")
	sb.WriteString(parseErr.Error())
	sb.WriteString("\n\nOriginal Studio instruction:\n")
	sb.WriteString(originalPrompt)
	sb.WriteString("\n\nMalformed response to repair:\n")
	sb.WriteString(excerpt)
	sb.WriteString("\n")
	return llm.Complete(ctx, sb.String())
}
