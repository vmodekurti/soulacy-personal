package studio

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/agentprompt"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// catalogAgentSet returns a case-insensitive set of the agent ids/names present
// in the catalog, so we can tell which agent nodes reference an EXISTING agent
// versus a brand-new helper the workflow needs us to create.
func catalogAgentSet(cat Catalog) map[string]bool {
	set := make(map[string]bool, len(cat.Agents))
	for _, a := range cat.Agents {
		if t := strings.TrimSpace(a); t != "" {
			set[strings.ToLower(t)] = true
		}
	}
	return set
}

// thinPrompt reports whether a system prompt is missing or too thin to be a
// real, reusable persona. The compiler is instructed to write rich prompts, but
// models sometimes emit a one-liner or nothing at all; this is the guardrail
// that decides when the deterministic synthesis must step in.
func thinPrompt(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return true
	}
	// A genuine persona is more than a label. Treat very short or
	// single-sentence prompts as thin.
	if len(p) < 40 {
		return true
	}
	return false
}

// ensureNewAgents guarantees that EVERY agent node referencing an agent that is
// not in the catalog has a corresponding, fully-populated NewAgent entry in the
// draft — with a real name, description, and a rich, reusable system prompt.
// This is the deterministic backstop behind the strengthened compiler prompt:
// even if the model forgets an agent or emits a blank/thin profile, no helper
// agent is ever saved blank. It is pure and side-effect-free except for
// mutating the passed draft, so it is fully unit-testable.
func ensureNewAgents(draft *Draft, cat Catalog) {
	if draft == nil {
		return
	}
	inCatalog := catalogAgentSet(cat)

	// Index existing NewAgents by lowercased id for quick lookup + in-place fix.
	byID := make(map[string]int, len(draft.NewAgents))
	for i, na := range draft.NewAgents {
		byID[strings.ToLower(strings.TrimSpace(na.ID))] = i
	}

	for _, node := range draft.Flow.Nodes {
		if node.Kind != "agent" {
			continue
		}
		agentID := strings.TrimSpace(node.Agent)
		if agentID == "" {
			continue
		}
		if inCatalog[strings.ToLower(agentID)] {
			continue // references a real, existing agent — nothing to synthesize
		}

		key := strings.ToLower(agentID)
		if idx, ok := byID[key]; ok {
			// The model provided a profile — fill only the gaps so we keep its
			// (usually better) content and only repair thin/blank fields.
			na := draft.NewAgents[idx]
			synth := SynthesizeAgent(agentID, node, draft.Name)
			if strings.TrimSpace(na.Name) == "" {
				na.Name = synth.Name
			}
			if strings.TrimSpace(na.Description) == "" {
				na.Description = synth.Description
			}
			if thinPrompt(na.SystemPrompt) {
				na.SystemPrompt = synth.SystemPrompt
			}
			draft.NewAgents[idx] = na
			continue
		}

		// No profile at all — synthesize a complete one and append it.
		synth := SynthesizeAgent(agentID, node, draft.Name)
		draft.NewAgents = append(draft.NewAgents, synth)
		byID[key] = len(draft.NewAgents) - 1
	}
}

// SynthesizeAgent deterministically builds a complete, reusable agent profile
// for a helper agent referenced by a workflow node, from the node's own
// description and task input plus the workflow name. It is the fallback used
// when the model failed to supply a quality persona — so a "Notifier" or
// "Summarizer" agent is never saved blank. Pure and unit-testable.
func SynthesizeAgent(agentID string, node sdkr.FlowNode, workflowName string) NewAgent {
	id := strings.TrimSpace(agentID)
	name := humanizeID(id)

	role := strings.TrimSpace(node.Description)
	task := strings.TrimSpace(node.Input)
	if prompt, ok := synthesizeDomainPrompt(id, name, role, task); ok {
		desc := role
		if desc == "" {
			desc = fmt.Sprintf("%s helper agent", name)
		}
		if len(desc) > 140 {
			desc = desc[:140]
		}
		return NewAgent{
			ID:           id,
			Name:         name,
			Description:  desc,
			SystemPrompt: agentprompt.EnsureShared(prompt),
		}
	}

	wf := strings.TrimSpace(workflowName)

	desc := role
	if desc == "" {
		desc = fmt.Sprintf("%s helper agent", name)
	}
	if len(desc) > 140 {
		desc = desc[:140]
	}

	var sb strings.Builder
	sb.WriteString("You are ")
	sb.WriteString(name)
	sb.WriteString(", a focused, reusable assistant")
	if wf != "" {
		sb.WriteString(" that supports the \"")
		sb.WriteString(wf)
		sb.WriteString("\" workflow")
	}
	sb.WriteString(". ")
	if role != "" {
		sb.WriteString("Your responsibility: ")
		sb.WriteString(ensureSentence(role))
		sb.WriteString(" ")
	}
	sb.WriteString("You receive a concrete task with any data it needs already provided in the message. ")
	sb.WriteString("Carry out exactly that task — do not invent extra steps or ask for information you were not given. ")
	if task != "" {
		hint := task
		if len(hint) > 220 {
			hint = hint[:220] + "…"
		}
		sb.WriteString("A typical request looks like: \"")
		sb.WriteString(collapseWhitespace(hint))
		sb.WriteString("\". ")
	}
	sb.WriteString("Produce a clear, well-structured result in the format the task asks for; default to concise plain text or markdown when no format is specified. ")
	sb.WriteString("If the input is empty, missing, or malformed, respond with a short, graceful fallback message instead of failing or fabricating content.")

	return NewAgent{
		ID:           id,
		Name:         name,
		Description:  desc,
		SystemPrompt: agentprompt.EnsureShared(sb.String()),
	}
}

func synthesizeDomainPrompt(id, name, role, task string) (string, bool) {
	haystack := strings.ToLower(strings.Join([]string{id, name, role, task}, " "))
	switch {
	case strings.Contains(haystack, "weather") || strings.Contains(haystack, "forecast") || strings.Contains(haystack, "meteorolog"):
		return weatherExpertPrompt(name), true
	default:
		return "", false
	}
}

func weatherExpertPrompt(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Weather Expert"
	}
	const fence = "```"
	return fmt.Sprintf(`You are %s, a decision-focused weather assistant inside Soulacy.
Your job is not merely to report conditions. Your job is to help the user decide what to do.

When given a resolved location and the user's intent, use the available weather tools to answer with practical, hyperlocal guidance. Choose the right tool path:
- Use current conditions when the user asks about now, today, or immediate conditions.
- Use forecast when the user asks about later today, tomorrow, this week, travel, outdoor plans, commute windows, events, or planning.
- Use alerts when safety risk may matter: storms, severe weather, heat, cold, wind, flooding, snow/ice, poor air quality, or travel disruption.
- If the request is ambiguous, still answer using the most useful current plus near-term forecast data, and state the assumption briefly.

Make the answer uniquely useful by including these sections when data supports them:
1. Direct answer: one sentence that answers the user's real question.
2. Decision guidance: what the user should do, avoid, bring, reschedule, or watch.
3. Best window / risk window: the safest or most comfortable time window, and the riskiest time window.
4. Key conditions: temperature, feels-like temperature, precipitation chance, wind/gusts, humidity, UV, visibility, and notable conditions when available.
5. Confidence: high/medium/low, with a short reason such as model agreement, near-term timing, or uncertainty in precipitation timing.
6. Alerts and safety: mention official/severe alerts or practical safety concerns if relevant.
7. Planning note: connect the weather to common activities such as commute, walk/run, outdoor dining, sports, gardening, flying, driving, or events when relevant.

Formatting:
- Use concise Markdown with clear headings.
- Prefer tables for hourly or multi-day summaries.
- When enough numeric time-series data is available, include one compact chart block using this format:
  %schart
  {"title":{"text":"..."},"xAxis":{"type":"category","data":["..."]},"yAxis":{"type":"value"},"series":[{"name":"Temperature (°F)","type":"line","data":[...]}]}
  %s
- Do not invent measurements that are not present in tool data. If a metric is unavailable, omit it.
- Do not over-explain your tool usage. Focus on the user's decision.
- End with one useful next question only when it would improve the recommendation, for example asking about an event time, travel route, or activity.

If no data is returned from the tools, say the data is currently unavailable for that region and suggest one concrete way the user can rephrase the location.`, name, fence, fence)
}

// humanizeID turns a snake/kebab/camel agent id into a Title Case display name
// (e.g. "notifier" -> "Notifier", "news_summarizer" -> "News Summarizer").
func humanizeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "Helper Agent"
	}
	repl := strings.NewReplacer("_", " ", "-", " ", ".", " ")
	id = repl.Replace(id)
	fields := strings.Fields(id)
	for i, f := range fields {
		if f == "" {
			continue
		}
		fields[i] = strings.ToUpper(f[:1]) + f[1:]
	}
	if len(fields) == 0 {
		return "Helper Agent"
	}
	return strings.Join(fields, " ")
}

// ensureSentence capitalizes the first letter and appends a period if missing,
// so synthesized prose reads cleanly.
func ensureSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return s
}

// collapseWhitespace flattens runs of whitespace (including newlines) to single
// spaces so an injected task hint stays on one line in the persona prompt.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// agentNodeFor finds the flow node that delegates to this agent, so a
// synthesized persona can be written from what the step actually does rather
// than from the agent's id alone. Returns the zero node when the draft has no
// such step (a peer declared in new_agents but not yet wired) — SynthesizeAgent
// handles that, it just produces a more generic persona.
func agentNodeFor(draft Draft, agentID string) sdkr.FlowNode {
	want := strings.ToLower(strings.TrimSpace(agentID))
	for _, n := range draft.Flow.Nodes {
		if n.Kind == "agent" && strings.ToLower(strings.TrimSpace(n.Agent)) == want {
			return n
		}
	}
	// Fall back to the declared profile's own description so the persona still
	// says something specific about the job.
	for _, na := range draft.NewAgents {
		if strings.ToLower(strings.TrimSpace(na.ID)) == want {
			return sdkr.FlowNode{Description: na.Description}
		}
	}
	return sdkr.FlowNode{}
}
