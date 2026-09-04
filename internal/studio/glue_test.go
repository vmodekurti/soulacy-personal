package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestLooksLikeTransform(t *testing.T) {
	cases := []struct {
		desc, tool string
		want       bool
	}{
		{"Parse the search results and keep the top 5", "pick_top", true},
		{"Filter out duplicates", "dedupe_items", true},
		{"Send the digest to the user", "send_telegram", false},
		{"Fetch the latest stories from the API", "http_get", false},
		{"", "format_table", true},
		{"do the thing", "mystery", false},
	}
	for _, c := range cases {
		if got := looksLikeTransform(c.desc, c.tool); got != c.want {
			t.Errorf("looksLikeTransform(%q,%q)=%v want %v", c.desc, c.tool, got, c.want)
		}
	}
}

func TestEnsureCapabilities_ConvertsTransformHole(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "search", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "res"},
		{ID: "top5", Kind: "tool", Tool: "pick_top_articles",
			Description: "Parse the results and keep the top 5", Output: "articles"},
	}}}
	cat := Catalog{Tools: []string{"web_search"}} // pick_top_articles is unknown
	code := "def run(inputs):\n    return (inputs.get('res') or [])[:5]"
	n, notes := EnsureCapabilities(context.Background(), fakeLLM{out: code}, &draft, cat)
	if n != 1 {
		t.Fatalf("want 1 conversion, got %d (notes=%v)", n, notes)
	}
	got := draft.Flow.Nodes[1]
	if got.Kind != "python" || got.Tool != "" || !strings.Contains(got.Code, "def run") {
		t.Errorf("node not converted to python: %+v", got)
	}
	if got.ID != "top5" || got.Output != "articles" {
		t.Errorf("conversion must preserve id+output: %+v", got)
	}
}

func TestEnsureCapabilities_LeavesExternalHoleAlone(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "post", Kind: "tool", Tool: "post_to_blog", Description: "Publish the article to the blog", Output: "r"},
	}}}
	cat := Catalog{Tools: []string{"web_search"}}
	n, _ := EnsureCapabilities(context.Background(), fakeLLM{out: "def run(inputs):\n    return 1"}, &draft, cat)
	if n != 0 {
		t.Fatalf("external capability must not be fabricated; converted %d", n)
	}
	if draft.Flow.Nodes[0].Kind == "python" {
		t.Errorf("external tool node should be left as-is")
	}
}
