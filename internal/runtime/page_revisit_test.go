package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// One shopping question opened the same three pages about twenty times each
// (#403). A page is really opened twice at most; after that the run gets the
// text of the last visit and no browser session is started.
func TestRepeatedPageOpensAreAnsweredFromTheLastVisit(t *testing.T) {
	e := newTrustTestEngine(t, &trustSink{})
	opens := 0
	e.builtins = []BuiltinTool{{
		Name: "start_website_action", Parameters: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			opens++
			return fmt.Sprintf(`{"session_id":"web_%d","status":"ready","observation":{"title":"Deals","text":"Vacuum A is $199 today","url":"https://shop.example/deals"}}`, opens), nil
		},
	}}
	def := &agent.Definition{ID: GenieAgentID}
	ctx := withProgressGuard(context.Background(), def)
	seen := map[string]string{}
	var mu sync.Mutex
	open := func(i int, url string) message.ToolResult {
		return e.executeOneToolCall(ctx, def, "sess-pages", message.ToolCall{
			ID: fmt.Sprintf("call-%d", i), Name: "start_website_action", Arguments: map[string]any{"url": url},
		}, seen, &mu)
	}

	open(1, "https://shop.example/deals")
	open(2, "https://shop.example/deals/")
	if opens != 2 {
		t.Fatalf("first two visits should really open the page, opened %d times", opens)
	}
	third := open(3, "https://Shop.example/deals#top")
	if opens != 2 {
		t.Fatalf("third visit started another browser session (%d opens)", opens)
	}
	for _, want := range []string{"already_read", "Vacuum A is $199 today", "do not call close_website_action"} {
		if !strings.Contains(third.Content, want) {
			t.Errorf("third visit result is missing %q:\n%s", want, third.Content)
		}
	}
	if third.IsError || strings.Contains(third.Content, "web_") {
		t.Errorf("a recalled page must not be an error or name a session:\n%s", third.Content)
	}

	// The text is not resent forever: that would only refill the window.
	var last message.ToolResult
	for i := 4; i <= 8; i++ {
		last = open(i, "https://shop.example/deals")
	}
	if opens != 2 || strings.Contains(last.Content, "Vacuum A") || !strings.Contains(last.Content, "Write the answer now") {
		t.Errorf("after several recalls the run should be told to answer (opens=%d):\n%s", opens, last.Content)
	}

	// A different page is unaffected.
	open(9, "https://shop.example/other")
	if opens != 3 {
		t.Errorf("a new page should open normally, opens=%d", opens)
	}
}

func TestFailedPageOpensDoNotCountAsVisits(t *testing.T) {
	g := &progressGuard{}
	call := message.ToolCall{Name: "start_website_action", Arguments: map[string]any{"url": "https://shop.example/x"}}
	for i := 0; i < 5; i++ {
		g.rememberPage(call, "error: timeout", true)
	}
	if _, recalled := g.recallPage(call); recalled {
		t.Fatal("a page that never opened must stay openable")
	}
	if _, recalled := (*progressGuard)(nil).recallPage(call); recalled {
		t.Fatal("no guard means no recall")
	}
}
