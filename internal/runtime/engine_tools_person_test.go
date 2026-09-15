package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/person"
	"github.com/soulacy/soulacy/pkg/message"
)

func personTestEngine(t *testing.T) (*Engine, person.Store) {
	t.Helper()
	store, err := person.OpenSQLite(filepath.Join(t.TempDir(), "person.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(); personStore.Store(nil) })
	e := &Engine{}
	e.SetPersonModel(store)
	return e, store
}

// operatorCtx mirrors what an interactive run carries: a principal and the
// inbound message (which is where the agent id comes from).
func operatorCtx(agentID string) context.Context {
	return operatorCtxSaying(agentID, "")
}

// operatorCtxSaying is the same, with the person's words for this turn.
func operatorCtxSaying(agentID, said string) context.Context {
	ctx := WithPrincipal(context.Background(), Principal{Subject: "kai", Role: "operator"})
	msg := message.Message{AgentID: agentID, Channel: "http"}
	if said != "" {
		msg.Parts = message.Text(said)
	}
	return context.WithValue(ctx, inboundMsgKey{}, msg)
}

func personTool(t *testing.T, e *Engine, name string) BuiltinTool {
	t.Helper()
	for _, tool := range e.buildPersonBuiltins() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not built", name)
	return BuiltinTool{}
}

func TestPersonModelToolReadsProseNotJSON(t *testing.T) {
	e, store := personTestEngine(t)
	ctx := operatorCtx("planner")
	if _, err := store.PutAll(ctx, []person.Entry{
		{Owner: "kai", Section: person.SectionIdentity, Key: "home", Summary: "Home is 14 Oak Street", Source: person.SourceManual},
		{Owner: "kai", Section: person.SectionCommitments, Key: "proposal", Summary: "Owes Priya the proposal by Friday", Source: "sense:email"},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := personTool(t, e, "person.model").Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Home is 14 Oak Street") || !strings.Contains(out, "Owes Priya") {
		t.Fatalf("model should render both entries:\n%s", out)
	}
	if strings.Contains(out, "{") {
		t.Fatalf("the tool must answer in prose:\n%s", out)
	}

	// Narrowed by section.
	out, err = personTool(t, e, "person.model").Handler(ctx, map[string]any{"sections": []any{"commitments"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Oak Street") || !strings.Contains(out, "Owes Priya") {
		t.Fatalf("section filter:\n%s", out)
	}

	// Narrowed by query.
	out, err = personTool(t, e, "person.model").Handler(ctx, map[string]any{"query": "priya proposal"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Owes Priya") || strings.Contains(out, "Oak Street") {
		t.Fatalf("query filter:\n%s", out)
	}
	if out, err := personTool(t, e, "person.model").Handler(ctx, map[string]any{"query": "sailing"}); err != nil || !strings.Contains(out, "Nothing in what Soulacy knows") {
		t.Fatalf("a miss should say so plainly: %q %v", out, err)
	}
}

func TestPersonObserveRecordsUnderTheAgentsName(t *testing.T) {
	e, store := personTestEngine(t)
	ctx := operatorCtx("planner")

	out, err := personTool(t, e, "person.observe").Handler(ctx, map[string]any{
		"section": "preferences", "key": "meetings", "summary": "Prefers morning meetings", "confidence": 0.8,
	})
	if err != nil || !strings.Contains(out, "Recorded") {
		t.Fatalf("observe: %q %v", out, err)
	}
	entry, err := store.Get(ctx, "kai", person.SectionPreferences, "meetings")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Source != "agent:planner" {
		t.Fatalf("the entry must name the agent that inferred it, got %q", entry.Source)
	}
	if entry.Confidence != 0.8 {
		t.Fatalf("confidence not carried: %v", entry.Confidence)
	}

	// An expiring observation, for anything that goes stale.
	if _, err := personTool(t, e, "person.observe").Handler(ctx, map[string]any{
		"section": "state", "key": "now", "summary": "In a meeting", "expires_in_hours": 1,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(ctx, "kai", person.SectionState, "now")
	if err != nil || state.ExpiresAt == nil {
		t.Fatalf("state should expire: %+v %v", state, err)
	}

	// What the person said themselves is not overwritten, and the agent is told.
	if _, err := store.Put(ctx, person.Entry{
		Owner: "kai", Section: person.SectionPreferences, Key: "seats",
		Summary: "Never a middle seat", Source: person.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	out, err = personTool(t, e, "person.observe").Handler(ctx, map[string]any{
		"section": "preferences", "key": "seats", "summary": "Does not mind middle seats",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Not recorded") || !strings.Contains(out, "Never a middle seat") {
		t.Fatalf("the agent must learn it was overruled, and what stands: %q", out)
	}
}

func TestPersonToolsRefuseWithoutIdentityOrStore(t *testing.T) {
	e, _ := personTestEngine(t)

	// No principal: the tool cannot know whose model to read.
	anonymous := context.WithValue(context.Background(), inboundMsgKey{}, message.Message{AgentID: "planner", Channel: "slack"})
	if _, err := personTool(t, e, "person.model").Handler(anonymous, map[string]any{}); err == nil {
		t.Fatal("an unidentified caller must not read a person model")
	}

	// No store configured.
	e.SetPersonModel(nil)
	if _, err := personTool(t, e, "person.model").Handler(operatorCtx("planner"), map[string]any{}); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled gateway should say so: %v", err)
	}
}

func TestPersonToolsAreOptInPerAgent(t *testing.T) {
	e, _ := personTestEngine(t)
	for _, tool := range e.buildPersonBuiltins() {
		if tool.Gate != "person" {
			t.Fatalf("%s must be gated so an agent opts in explicitly, got %q", tool.Name, tool.Gate)
		}
	}
}

func TestPersonObserveWillNotRecordAnInventedQuote(t *testing.T) {
	e, store := personTestEngine(t)
	ctx := operatorCtxSaying("getting-to-know-you", "Call me Kai. I live in Oak Park.")
	observe := personTool(t, e, "person.observe")

	// The quote is really there, in the person's own words.
	out, err := observe.Handler(ctx, map[string]any{
		"section": "identity", "key": "home", "summary": "Lives in Oak Park",
		"quote": "I live in Oak Park", "confidence": 1,
	})
	if err != nil || !strings.Contains(out, "Recorded") {
		t.Fatalf("a true quote should be accepted: %q %v", out, err)
	}
	entry, err := store.Get(ctx, "kai", person.SectionIdentity, "home")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Value["said"] != "I live in Oak Park" {
		t.Fatalf("the quote should be kept as provenance: %+v", entry.Value)
	}
	if entry.Confidence != 1 {
		t.Fatalf("a quoted entry keeps its confidence: %v", entry.Confidence)
	}

	// The model answers its own question and quotes words nobody said.
	if _, err := observe.Handler(ctx, map[string]any{
		"section": "routine", "key": "weekday.start", "summary": "Starts work around 8:30",
		"quote": "I start at 8:30 every weekday", "confidence": 1,
	}); err == nil {
		t.Fatal("an invented quote must be refused")
	}
	if _, err := store.Get(ctx, "kai", person.SectionRoutine, "weekday.start"); err == nil {
		t.Fatal("nothing should have been written")
	}

	// Slight paraphrase of real words is fine; punctuation and case are not
	// what makes a quote true.
	if _, err := observe.Handler(ctx, map[string]any{
		"section": "identity", "key": "name", "summary": "Goes by Kai",
		"quote": "call me kai", "confidence": 1,
	}); err != nil {
		t.Fatalf("a loose match of real words should pass: %v", err)
	}
}

func TestPersonObserveKeepsUnquotedClaimsAsGuesses(t *testing.T) {
	e, store := personTestEngine(t)
	ctx := operatorCtxSaying("planner", "I have been really busy this week.")

	out, err := personTool(t, e, "person.observe").Handler(ctx, map[string]any{
		"section": "preferences", "key": "meetings", "summary": "Prefers morning meetings", "confidence": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "guess") {
		t.Fatalf("an unquoted claim of certainty should say it was downgraded: %q", out)
	}
	entry, err := store.Get(ctx, "kai", person.SectionPreferences, "meetings")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Confidence >= 0.9 {
		t.Fatalf("certainty must be earned by a quote: %v", entry.Confidence)
	}

	// A modest claim is left exactly as the agent stated it.
	if _, err := personTool(t, e, "person.observe").Handler(ctx, map[string]any{
		"section": "preferences", "key": "pace", "summary": "Seems stretched this week", "confidence": 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	pace, _ := store.Get(ctx, "kai", person.SectionPreferences, "pace")
	if pace.Confidence != 0.5 {
		t.Fatalf("an honest guess should be untouched: %v", pace.Confidence)
	}
}
