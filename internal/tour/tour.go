package tour

// tour.go — assembling one page's telling.

import "fmt"

// Pages lists every screen the tour can narrate.
func Pages() []string {
	out := make([]string, 0, len(pages))
	for id := range pages {
		out = append(out, id)
	}
	return out
}

// Has reports whether a page has a story.
func Has(id string) bool {
	_, ok := pages[id]
	return ok
}

// Narrate builds the story for one page against the current install.
//
// The shape is always the same, so a reader learns it once: where this fits in
// the chain, what it gives you, where you actually are, and what to do next.
func Narrate(id string, st InstallState) (PageTour, bool) {
	p, ok := pages[id]
	if !ok {
		return PageTour{}, false
	}

	t := PageTour{
		Page:     id,
		Chapter:  p.stage.String(),
		Outcome:  Outcome,
		Position: position(p.stage, st),
	}

	t.Beats = append(t.Beats, Beat{Heading: "Why this screen exists", Text: p.role + " " + p.contribution})

	// The adaptive middle: what is true here, right now.
	state := p.whenEmpty(st)
	if p.stage.Met(st) && hasContent(id, st) {
		state = p.whenUsed(st)
	}
	t.Beats = append(t.Beats, Beat{Heading: "Where you are", Text: state})

	// If an earlier link is missing, say so plainly — and hand over the action
	// that unblocks it rather than the one this page would normally offer.
	if blocker, missing := FirstUnmet(st); missing && blocker < p.stage {
		t.Blocked = blocker.String()
		t.Beats = append(t.Beats, Beat{
			Heading: "First, though",
			Text:    blockedText(blocker),
		})
		if bp, ok := pages[pageForStage(blocker)]; ok && bp.nextAction != "" {
			t.NextAction, t.NextLabel = bp.nextAction, bp.nextLabel
		}
		return t, true
	}

	t.NextAction, t.NextLabel = p.nextAction, p.nextLabel
	return t, true
}

// hasContent reports whether THIS page has anything in it, which is a different
// question from whether its stage is satisfied — Skills and Knowledge both
// satisfy StageMaterial, so an install with skills but no knowledge bases must
// still get the empty telling on the Knowledge page.
func hasContent(id string, st InstallState) bool {
	switch id {
	case "knowledge":
		return st.KnowledgeBases > 0
	case "skills":
		return st.Skills > 0
	case "mcp":
		return st.MCPServers > 0
	case "pluginmgr":
		return st.Plugins > 0
	case "workboard":
		return st.OpenTasks > 0
	case "memory":
		return st.Runs > 0
	case "queues":
		return st.Runs > 0
	case "chat", "templates", "logs", "browser", "mobile", "secrets", "config", "onboarding", "dashboard":
		return st.Runs > 0
	default:
		return true
	}
}

// pageForStage names the screen that unblocks a stage.
func pageForStage(s Stage) string {
	switch s {
	case StageBrain:
		return "providers"
	case StagePlan:
		return "studio"
	case StageMaterial:
		return "mcp"
	case StageMouth:
		return "channels"
	case StageClock:
		return "schedule"
	default:
		return "activity"
	}
}

func blockedText(s Stage) string {
	switch s {
	case StageBrain:
		return "Nothing here can work yet, because no model is connected. Everything else waits on that one setting."
	case StagePlan:
		return "There is no agent yet, so there is nothing for this screen to act on. Build one in Studio first — this becomes useful the moment you have."
	case StageMaterial:
		return "Your agents have no tools, skills or knowledge beyond the built-ins. That is fine for a first agent, and it is the ceiling you will hit next."
	case StageMouth:
		return "No destination is configured, so whatever your agents produce stays inside this box. That is usually the reason a working agent still feels like it does nothing."
	case StageClock:
		return "Nothing is scheduled yet, so your agents only run when you ask. Worth fixing once one of them earns the trust."
	default:
		return "Nothing has run yet, so there is nothing to look at here. Come back after the first run."
	}
}

// position tells the reader where in the chain they are standing, and — the
// useful part — whether this is the link they are missing.
func position(s Stage, st InstallState) string {
	blocker, missing := FirstUnmet(st)
	switch {
	case missing && blocker == s:
		return fmt.Sprintf("the %s of the chain — and the link you are missing", s)
	case missing && s < blocker:
		return fmt.Sprintf("the %s of the chain — already in place", s)
	case missing:
		return fmt.Sprintf("the %s of the chain — comes after the part you are missing", s)
	default:
		return fmt.Sprintf("the %s of the chain — the whole chain is in place", s)
	}
}
