package learning

import (
	"regexp"
	"strings"
)

// BuildInput is the distilled material from one successful run.
type BuildInput struct {
	AgentID      string
	AgentName    string
	SessionID    string
	Channel      string
	UserText     string
	ReplyText    string
	ToolsUsed    []string
	Source       string
	MinChars     int
	MaxProposals int
}

var listLikeLine = regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+[.)])\s+\S+`)

// BuildProposals turns one completed run into reviewable memory/procedure/skill
// proposals. It is intentionally deterministic and conservative: proposals are
// drafts for human review, not automatic memory mutation.
func BuildProposals(in BuildInput) []Proposal {
	userText := strings.TrimSpace(in.UserText)
	replyText := strings.TrimSpace(in.ReplyText)
	minChars := in.MinChars
	if minChars <= 0 {
		minChars = 160
	}
	if len(userText)+len(replyText) < minChars {
		return nil
	}
	maxProposals := in.MaxProposals
	if maxProposals <= 0 || maxProposals > 4 {
		maxProposals = 3
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "post_run"
	}
	meta := map[string]string{
		"channel": in.Channel,
		"agent":   in.AgentName,
	}
	if len(in.ToolsUsed) > 0 {
		meta["tools_used"] = strings.Join(uniqueStrings(in.ToolsUsed), ",")
	}

	proposals := []Proposal{{
		AgentID:    in.AgentID,
		SessionID:  in.SessionID,
		Kind:       "memory",
		Title:      titleFromTask(userText),
		Content:    memoryContent(userText, replyText, in.ToolsUsed),
		Confidence: 0.68,
		Source:     source,
		Meta:       cloneMeta(meta),
	}}
	if looksProcedural(replyText, in.ToolsUsed) && maxProposals > 1 {
		proposals = append(proposals, Proposal{
			AgentID:    in.AgentID,
			SessionID:  in.SessionID,
			Kind:       "procedure",
			Title:      "Reusable response procedure",
			Content:    procedureContent(userText, replyText, in.ToolsUsed),
			Confidence: 0.58,
			Source:     source,
			Meta:       cloneMeta(meta),
		})
	}
	if looksProcedural(replyText, in.ToolsUsed) && maxProposals > 2 {
		slug := skillSlugFromTask(userText, in.AgentID)
		smeta := cloneMeta(meta)
		smeta["skill_name"] = slug
		smeta["filename"] = "SKILL.md"
		proposals = append(proposals, Proposal{
			AgentID:    in.AgentID,
			SessionID:  in.SessionID,
			Kind:       "skill",
			Title:      "Installable skill draft: " + slug,
			Content:    skillProposalContent(slug, userText, replyText, in.AgentName, in.ToolsUsed),
			Confidence: 0.54,
			Source:     source,
			Meta:       smeta,
		})
	}
	if len(proposals) > maxProposals {
		proposals = proposals[:maxProposals]
	}
	return proposals
}

func memoryContent(task, reply string, tools []string) string {
	parts := []string{
		"Task: " + truncateForLearning(task, 700),
		"",
		"Outcome: " + truncateForLearning(reply, 1400),
	}
	if len(tools) > 0 {
		parts = append(parts, "", "Tools used: "+strings.Join(uniqueStrings(tools), ", "))
	}
	return strings.Join(parts, "\n")
}

func looksProcedural(s string, tools []string) bool {
	lower := strings.ToLower(s)
	return listLikeLine.MatchString(s) ||
		strings.Contains(lower, "steps") ||
		strings.Contains(lower, "checklist") ||
		strings.Contains(lower, "workflow") ||
		len(tools) >= 2
}

func procedureContent(task, reply string, tools []string) string {
	lines := []string{
		"# Candidate Procedure",
		"",
		"- Use this when a future task resembles: " + truncateForLearning(task, 240),
		"- Preserve the useful response shape or checklist from the successful run.",
		"- Verify fresh facts before reusing dated facts, prices, schedules, or external data.",
	}
	if len(tools) > 0 {
		lines = append(lines, "- Prefer these tools when still relevant: "+strings.Join(uniqueStrings(tools), ", "))
	}
	lines = append(lines,
		"",
		"## Source Excerpt",
		truncateForLearning(reply, 900),
	)
	return strings.Join(lines, "\n")
}

func skillProposalContent(slug, task, reply, agentName string, tools []string) string {
	desc := "Reusable workflow learned from a successful Soulacy agent run."
	if task = strings.TrimSpace(task); task != "" {
		desc = "Use when a task resembles: " + truncateForLearning(task, 180)
	}
	lines := []string{
		"---",
		"name: " + slug,
		"description: " + yamlQuote(desc),
		"compatibility: Soulacy Agent Skills",
		"metadata:",
		"  generated_by: soulacy-learning",
		"  source_agent: " + yamlQuote(agentName),
		"---",
		"",
		"# " + titleFromTask(task),
		"",
		"Use this skill when the user asks for work similar to the source task below.",
		"",
		"## Workflow",
		"- Restate the user's objective and identify any required inputs.",
		"- Follow the useful structure from the source run while refreshing facts, prices, schedules, or other time-sensitive information.",
	}
	if len(tools) > 0 {
		lines = append(lines, "- Consider these tools from the source run when appropriate: "+strings.Join(uniqueStrings(tools), ", "))
	} else {
		lines = append(lines, "- Use available Soulacy tools and channels only when they are relevant to the task.")
	}
	lines = append(lines,
		"- Return the final answer with clear assumptions, outputs, and next actions.",
		"",
		"## Source Task",
		truncateForLearning(task, 600),
		"",
		"## Source Pattern",
		truncateForLearning(reply, 1200),
	)
	return strings.Join(lines, "\n")
}

func skillSlugFromTask(task, fallback string) string {
	base := strings.ToLower(strings.TrimSpace(task))
	if base == "" {
		base = fallback
	}
	replacer := regexp.MustCompile(`[^a-z0-9]+`)
	base = replacer.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "learned-skill"
	}
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}
	if base == "" {
		base = "learned-skill"
	}
	return base
}

func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func titleFromTask(task string) string {
	task = strings.Join(strings.Fields(task), " ")
	if task == "" {
		return "Post-run learning"
	}
	if len(task) <= 72 {
		return task
	}
	return strings.TrimSpace(task[:72]) + "..."
}

func truncateForLearning(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func cloneMeta(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}
