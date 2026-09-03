package studio

import "testing"

func TestImproveGeneratedAgentNameUsesKnowledgeBasePurpose(t *testing.T) {
	draft := Draft{
		Name:      "Knowledge Ingestion Agent",
		Intent:    "Ingest HBR articles about AI into the executive research knowledge base.",
		Knowledge: []string{"executive_research"},
	}
	ImproveGeneratedAgentName(&draft, Catalog{})
	if draft.Name != "Executive Research Knowledge Curator" {
		t.Fatalf("name = %q", draft.Name)
	}
}

func TestImproveGeneratedAgentNameUsesTaskSubject(t *testing.T) {
	draft := Draft{Name: "Soulacy Agent", Intent: "Monitor competitor pricing and alert me to important changes."}
	ImproveGeneratedAgentName(&draft, Catalog{})
	if draft.Name != "Competitive Monitor" {
		t.Fatalf("name = %q", draft.Name)
	}
}

func TestImproveGeneratedAgentNameCombinesDomainAndAction(t *testing.T) {
	draft := Draft{Name: "Stock Advisor", Intent: "Monitor stock earnings and notify me when guidance changes."}
	ImproveGeneratedAgentName(&draft, Catalog{})
	if draft.Name != "Market Monitor" {
		t.Fatalf("name = %q", draft.Name)
	}
}

func TestImproveGeneratedAgentNamePreservesExplicitName(t *testing.T) {
	draft := Draft{Name: "Knowledge Ingestion Agent", Intent: "Create an agent named Boardroom Librarian. Ingest executive documents."}
	ImproveGeneratedAgentName(&draft, Catalog{Agents: []string{"boardroom-librarian"}})
	if draft.Name != "Boardroom Librarian" {
		t.Fatalf("explicit name changed to %q", draft.Name)
	}
}

func TestImproveGeneratedAgentNamePreservesExistingAgentEdit(t *testing.T) {
	draft := Draft{ID: "existing", Name: "My Carefully Chosen Name", Intent: "monitor stocks"}
	ImproveGeneratedAgentName(&draft, Catalog{Agents: []string{"existing"}})
	if draft.Name != "My Carefully Chosen Name" {
		t.Fatalf("existing agent name changed to %q", draft.Name)
	}
}

func TestImproveGeneratedAgentNameAvoidsExistingNameCollision(t *testing.T) {
	draft := Draft{Name: "Research Agent", Intent: "Research the latest AI news."}
	ImproveGeneratedAgentName(&draft, Catalog{Agents: []string{"ai-research-curator"}})
	if draft.Name == "AI Research Curator" || draft.Name == "Research Agent" {
		t.Fatalf("colliding generated name was not disambiguated: %q", draft.Name)
	}
}
