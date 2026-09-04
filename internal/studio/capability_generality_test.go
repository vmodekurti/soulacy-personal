package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// These tests exist to answer one question: was the "workflows ignore my MCP
// server" fix made for the travel case that reported it, or for any capability?
//
// Every case below is a domain the code has never seen — clinical, legal,
// logistics, manufacturing — with server and tool names that share no words
// with travel, search, or digests. If any of this were special-cased to trvl,
// these would fail.

type domainCase struct {
	name     string
	server   string
	tool     string
	toolDesc string
	intent   string
	// skill that genuinely fits the intent, plus a decoy that only shares
	// ordinary English with it.
	topicalSkill CatalogSkill
	decoySkill   CatalogSkill
}

func domainCases() []domainCase {
	return []domainCase{
		{
			name: "clinical", server: "ehr", tool: "mcp__ehr__patient_lookup",
			toolDesc: "Look up patient encounters and discharge summaries.",
			intent: "Every Monday at 6am pull discharge summaries for readmitted patients " +
				"using the ehr MCP patient_lookup tool and post the list to slack",
			topicalSkill: CatalogSkill{Name: "discharge-auditor", Description: "Audit patient discharge summaries and readmission coding."},
			decoySkill:   CatalogSkill{Name: "options-payoff", Description: "Compute option payoff data and answer user questions about results."},
		},
		{
			name: "legal", server: "docket", tool: "mcp__docket__case_search",
			toolDesc: "Search court dockets and filings by jurisdiction.",
			intent: "Each weekday morning search new filings in the docket MCP case_search tool " +
				"for our matters and email a summary",
			topicalSkill: CatalogSkill{Name: "filing-digest", Description: "Summarise new court filings and docket entries by matter."},
			decoySkill:   CatalogSkill{Name: "earnings-recap", Description: "Recap an earnings call and answer user questions about results."},
		},
		{
			name: "logistics", server: "fleetops", tool: "mcp__fleetops__shipment_status",
			toolDesc: "Retrieve shipment and carrier exception status.",
			intent: "Every day at 5am check delayed shipments with the fleetops MCP shipment_status " +
				"tool and send exceptions to telegram",
			topicalSkill: CatalogSkill{Name: "carrier-exception-report", Description: "Report delayed shipment and carrier exception detail."},
			decoySkill:   CatalogSkill{Name: "tradingview-reader", Description: "Reader that searches chart data and returns results to an agent."},
		},
		{
			name: "manufacturing", server: "scada", tool: "mcp__scada__sensor_history",
			toolDesc: "Read machine sensor history and fault codes.",
			intent: "Hourly, pull fault codes from the scada MCP sensor_history tool and " +
				"alert maintenance on slack",
			topicalSkill: CatalogSkill{Name: "fault-code-triage", Description: "Triage machine fault codes and sensor anomalies for maintenance."},
			decoySkill:   CatalogSkill{Name: "funda-data", Description: "Fundamental data search for an agent to request company information."},
		},
	}
}

func (d domainCase) catalog() Catalog {
	cat := Catalog{
		Tools: []string{"web_search", "fetch_url", "channel.send"},
		MCP: []CatalogMCPServer{{
			Server: d.server,
			Tools:  []CatalogMCPTool{{Name: d.tool, Description: d.toolDesc}},
		}},
		Channels: []string{"telegram", "slack"},
		Skills:   []CatalogSkill{d.topicalSkill, d.decoySkill},
	}
	// Push past the trim cap so the shortlist logic engages.
	for i := 0; i < 220; i++ {
		cat.Skills = append(cat.Skills, CatalogSkill{
			Name:        fmt.Sprintf("filler-%03d", i),
			Description: "A tool that helps a user request data and generate a result report.",
		})
	}
	return cat
}

//  1. The deterministic planner's fetch step must reach for whatever MCP server
//     the intent named — not web_search, and not a hardcoded vendor.
func TestDeterministicSearchToolIsCapabilityAgnostic(t *testing.T) {
	for _, d := range domainCases() {
		t.Run(d.name, func(t *testing.T) {
			if got := deterministicSearchTool(d.intent, d.catalog()); got != d.tool {
				t.Errorf("search tool = %q, want the named MCP tool %q", got, d.tool)
			}
		})
	}
}

//  2. The coverage retry must fire for any named capability, and must name THAT
//     capability in the corrective prompt.
func TestCoverageRetryIsCapabilityAgnostic(t *testing.T) {
	for _, d := range domainCases() {
		t.Run(d.name, func(t *testing.T) {
			llm := &domainLLM{want: d.tool}
			res, err := RunGeneratePipeline(context.Background(), llm, d.intent, d.catalog(), PipelineOptions{})
			if err != nil {
				t.Fatalf("pipeline: %v", err)
			}
			used := draftToolSet(res.Compile.Workflow)
			var ok, generic bool
			for tool := range used {
				if strings.EqualFold(tool, d.tool) {
					ok = true
				}
				if strings.EqualFold(tool, "web_search") {
					generic = true
				}
			}
			if !ok {
				t.Errorf("graph does not use %q; tools = %v", d.tool, used)
			}
			if generic {
				t.Errorf("graph still substituted web_search; tools = %v", used)
			}
			last := llm.prompts[len(llm.prompts)-1]
			if !strings.Contains(last, "CORRECTION") || !strings.Contains(last, d.tool) {
				t.Errorf("retry prompt did not name %q", d.tool)
			}
		})
	}
}

//  3. The shortlist must surface the on-topic skill and drop the one that only
//     shares ordinary English — in every domain, from the same 222-skill install.
func TestShortlistIsCapabilityAgnostic(t *testing.T) {
	for _, d := range domainCases() {
		t.Run(d.name, func(t *testing.T) {
			out := FilterCatalogForIntent(d.intent, d.catalog())
			var kept []string
			for _, s := range out.Skills {
				kept = append(kept, s.Name)
			}
			joined := strings.Join(kept, ",")
			if !strings.Contains(joined, d.topicalSkill.Name) {
				t.Errorf("on-topic skill %q was not shortlisted; kept = %v", d.topicalSkill.Name, kept)
			}
			if strings.Contains(joined, d.decoySkill.Name) {
				t.Errorf("decoy %q was shortlisted; kept = %v", d.decoySkill.Name, kept)
			}
			for _, k := range kept {
				if strings.HasPrefix(k, "filler-") {
					t.Errorf("zero-signal filler %q padded the shortlist; kept = %v", k, kept)
					break
				}
			}
		})
	}
}

//  4. Model-chosen skills must be relevance-checked in any domain, not just when
//     the request happens to be about travel.
func TestSkillRelevanceGuardIsCapabilityAgnostic(t *testing.T) {
	for _, d := range domainCases() {
		t.Run(d.name, func(t *testing.T) {
			cat := d.catalog()
			draft := &Draft{RawIntent: d.intent}
			// A builder that lists a large slice of the catalogue.
			draft.Skills = []string{d.topicalSkill.Name, d.decoySkill.Name}
			for i := 0; i < 8; i++ {
				draft.Skills = append(draft.Skills, fmt.Sprintf("filler-%03d", i))
			}
			groundSkills(draft, cat)

			var keptTopical, keptDecoy bool
			for _, s := range draft.Skills {
				if s == d.topicalSkill.Name {
					keptTopical = true
				}
				if s == d.decoySkill.Name {
					keptDecoy = true
				}
			}
			if !keptTopical {
				t.Errorf("dropped the on-topic skill %q; kept = %v", d.topicalSkill.Name, draft.Skills)
			}
			if keptDecoy {
				t.Errorf("kept unrelated skill %q; kept = %v", d.decoySkill.Name, draft.Skills)
			}
		})
	}
}

// 5. The build-spec panel must report whatever server was named, for any domain.
func TestBuildSpecCapabilitiesAreCapabilityAgnostic(t *testing.T) {
	for _, d := range domainCases() {
		t.Run(d.name, func(t *testing.T) {
			spec := ExtractBuildSpecFrom(d.intent, d.catalog())
			if got := strings.Join(spec.Integrations, ","); !strings.Contains(got, d.server) {
				t.Errorf("capabilities = %q, want the named server %q", got, d.server)
			}
		})
	}
}

// agentJSON is the AGENT form of a draft — a tool allowlist, no flow. These
// intents resolve to strategy "auto", so the pipeline calls CompileAgent and a
// workflow-shaped reply is rejected before the coverage check is ever reached.
func agentJSON(tool string) string {
	d := Draft{
		Name:     "Domain Agent",
		Strategy: "auto",
		Trigger:  Trigger{Type: "schedule", Config: map[string]any{"cron": "0 6 * * 1-5"}},
		Tools:    []string{tool, "channel.send"},
		Channels: []string{"telegram"},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// domainLLM ignores the catalogue until the omission is named — the behaviour
// that was reported, reproduced per domain.
type domainLLM struct {
	want    string
	prompts []string
}

func (m *domainLLM) Complete(ctx context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	if strings.Contains(prompt, "CORRECTION") && strings.Contains(prompt, m.want) {
		return agentJSON(m.want), nil
	}
	return agentJSON("web_search"), nil
}
