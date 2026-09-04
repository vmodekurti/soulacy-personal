package learning

import "strings"
import "testing"

func TestBuildProposals_CreatesToolAwareSkillDraft(t *testing.T) {
	props := BuildProposals(BuildInput{
		AgentID:      "market-agent",
		AgentName:    "Market Agent",
		SessionID:    "sess-1",
		Channel:      "http",
		UserText:     "Create my morning market checklist",
		ReplyText:    "1. Check futures.\n2. Review earnings.\n3. Summarize risks.",
		ToolsUsed:    []string{"yfinance_quote", "news_search", "yfinance_quote"},
		MaxProposals: 3,
		MinChars:     20,
	})
	if len(props) != 3 {
		t.Fatalf("proposals = %d, want 3", len(props))
	}
	skill := props[2]
	if skill.Kind != "skill" {
		t.Fatalf("third proposal kind = %q, want skill", skill.Kind)
	}
	if !strings.Contains(skill.Content, "name: create-my-morning-market-checklist") {
		t.Fatalf("skill content missing slug:\n%s", skill.Content)
	}
	if !strings.Contains(skill.Content, "yfinance_quote, news_search") {
		t.Fatalf("skill content missing deduped tool provenance:\n%s", skill.Content)
	}
	if skill.Meta["tools_used"] != "yfinance_quote,news_search" {
		t.Fatalf("tools_used meta = %q", skill.Meta["tools_used"])
	}
}

func TestBuildProposals_TooShortReturnsNone(t *testing.T) {
	if got := BuildProposals(BuildInput{AgentID: "a", UserText: "hi", ReplyText: "ok", MinChars: 100}); len(got) != 0 {
		t.Fatalf("proposals = %#v, want none", got)
	}
}
