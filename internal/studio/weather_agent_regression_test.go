package studio

import (
	"context"
	"strconv"
	"testing"
)

const conversationalWeatherIntent = `A conversational weather agent that receives a user message with natural-language weather queries. It resolves a place with mcp__weather__search_location, calls the appropriate current conditions, forecast, summary, or alerts tool, and responds to the user via the same channel the query arrived on, including Telegram.`

func weatherAgentCatalog() Catalog {
	return Catalog{
		Tools:    []string{"web_search", "channel.send", "channel.status"},
		Channels: []string{"telegram"},
		MCP: []CatalogMCPServer{{Server: "weather", Tools: []CatalogMCPTool{
			{Name: "mcp__weather__search_location", Description: "Resolve a place"},
			{Name: "mcp__weather__get_current_conditions", Description: "Current weather"},
			{Name: "mcp__weather__get_forecast", Description: "Weather forecast"},
			{Name: "mcp__weather__get_weather_summary", Description: "Weather summary"},
			{Name: "mcp__weather__get_alerts", Description: "Weather alerts"},
		}}},
	}
}

func TestRefinePrompt_ConversationalWeatherOverridesModelWorkflowGuess(t *testing.T) {
	out := `{"refined_intent":` + strconv.Quote(conversationalWeatherIntent) + `,"summary":"weather assistant","recommended_mode":"workflow","mode_reason":"several ordered tools"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, conversationalWeatherIntent, weatherAgentCatalog())
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "auto" {
		t.Fatalf("conversational weather must use Auto regardless of the model's architecture guess; got %q", r.RecommendedMode)
	}
}

func TestCompileDeterministicAgent_WeatherUsesInboundChannelWithoutDeliveryTools(t *testing.T) {
	res, ok := CompileDeterministicAgent(conversationalWeatherIntent, weatherAgentCatalog(), "auto", nil)
	if !ok {
		t.Fatal("weather agent did not compile")
	}
	d := res.Workflow
	if d.Strategy != "auto" {
		t.Fatalf("strategy=%q, want auto", d.Strategy)
	}
	if d.Trigger.Type != "channel" {
		t.Fatalf("trigger=%q, want channel", d.Trigger.Type)
	}
	if len(d.Channels) != 1 || d.Channels[0] != "telegram" {
		t.Fatalf("channels=%v, want [telegram]", d.Channels)
	}
	for _, unwanted := range []string{"channel.send", "channel.status", "web_search"} {
		if containsFold(d.Tools, unwanted) {
			t.Errorf("same-channel weather reply should not receive %q; tools=%v", unwanted, d.Tools)
		}
	}
	for _, required := range []string{"mcp__weather__search_location", "mcp__weather__get_current_conditions", "mcp__weather__get_forecast"} {
		if !containsFold(d.Tools, required) {
			t.Errorf("weather agent missing %q; tools=%v", required, d.Tools)
		}
	}
	if len(d.ConfirmTools) != 0 {
		t.Errorf("ordinary same-channel replies need no confirmation tools: %v", d.ConfirmTools)
	}
}
