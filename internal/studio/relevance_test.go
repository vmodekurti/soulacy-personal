package studio

import (
	"strconv"
	"strings"
	"testing"
)

func TestFilterCatalog_SmallIsUnchanged(t *testing.T) {
	cat := Catalog{
		Skills:         []CatalogSkill{{Name: "yfinance"}, {Name: "weather"}},
		KnowledgeBases: []CatalogKB{{Name: "kb1"}},
	}
	out := FilterCatalogForIntent("anything", cat)
	if len(out.Skills) != 2 || len(out.KnowledgeBases) != 1 {
		t.Errorf("small catalog should be unchanged, got %d skills / %d kbs", len(out.Skills), len(out.KnowledgeBases))
	}
}

func TestFilterCatalog_TrimsAndKeepsRelevant(t *testing.T) {
	var skills []CatalogSkill
	// One clearly-relevant skill plus many noise skills over the cap.
	skills = append(skills, CatalogSkill{Name: "stocks", Description: "stock market quotes and finance data"})
	for i := 0; i < maxGroundedSkills+5; i++ {
		skills = append(skills, CatalogSkill{Name: "noise" + strconv.Itoa(i), Description: "unrelated capability"})
	}
	cat := Catalog{Skills: skills}
	out := FilterCatalogForIntent("get me stock market finance quotes", cat)
	// The cap is a ceiling, not a quota. This used to require EXACTLY 24, which
	// encoded the padding behaviour: the one relevant skill plus twenty-three
	// entries the fixture itself calls "noise" / "unrelated capability". Handing
	// those to the builder model is what produced agents carrying capabilities
	// nothing in the request pointed at, so they are now dropped rather than
	// used as filler.
	if len(out.Skills) > maxGroundedSkills {
		t.Fatalf("trim exceeded the cap: got %d, max %d", len(out.Skills), maxGroundedSkills)
	}
	if len(out.Skills) == 0 {
		t.Fatal("trim dropped everything, including the relevant skill")
	}
	found := false
	for _, s := range out.Skills {
		if s.Name == "stocks" {
			found = true
		}
		if strings.HasPrefix(s.Name, "noise") {
			t.Errorf("unrelated skill %q was kept as filler", s.Name)
		}
	}
	if !found {
		t.Error("relevant skill 'stocks' was dropped during trim")
	}
}

func TestFilterCatalog_KeepsNamedSkill(t *testing.T) {
	var skills []CatalogSkill
	for i := 0; i < maxGroundedSkills+10; i++ {
		skills = append(skills, CatalogSkill{Name: "noise" + strconv.Itoa(i)})
	}
	// A named skill with no descriptive overlap must still be kept.
	skills = append(skills, CatalogSkill{Name: "zzqux"})
	cat := Catalog{Skills: skills}
	out := FilterCatalogForIntent("please use the zzqux skill", cat)
	found := false
	for _, s := range out.Skills {
		if s.Name == "zzqux" {
			found = true
		}
	}
	if !found {
		t.Error("explicitly named skill 'zzqux' should always be kept")
	}
}

func TestFilterCatalog_TrimsMCPToolsKeepsNamedServer(t *testing.T) {
	var tools []CatalogMCPTool
	for i := 0; i < maxGroundedMCPTools+10; i++ {
		tools = append(tools, CatalogMCPTool{Name: "mcp__big__t" + strconv.Itoa(i), Description: "noise"})
	}
	cat := Catalog{MCP: []CatalogMCPServer{
		{Server: "notebooklm", Tools: []CatalogMCPTool{{Name: "mcp__notebooklm__create", Description: "make a notebook"}}},
		{Server: "big", Tools: tools},
	}}
	out := FilterCatalogForIntent("create a notebooklm notebook", cat)
	// notebooklm (named) must survive with its tool.
	var nb *CatalogMCPServer
	for i := range out.MCP {
		if out.MCP[i].Server == "notebooklm" {
			nb = &out.MCP[i]
		}
	}
	if nb == nil || len(nb.Tools) == 0 {
		t.Fatalf("named server notebooklm should be kept: %+v", out.MCP)
	}
	if countMCPTools(out.MCP) > maxGroundedMCPTools {
		t.Errorf("MCP tools not capped: %d", countMCPTools(out.MCP))
	}
}
