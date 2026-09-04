package studio

import "testing"

// People do not write "conversational". They write "a conversation agent", "a
// chatbot", "an assistant". Matching the adjective literally meant the ordinary
// phrasings fell through to a manual trigger and a pipeline shape.
func TestConversationalIntentMatchesOrdinaryPhrasing(t *testing.T) {
	interactive := []string{
		"I want to develop a conversation agent that provides timely weather updates based on a place or a zipcode",
		"build a chatbot for internal IT questions",
		"a chat bot that looks up order status",
		"an assistant that helps staff find policy documents",
		"an interactive agent for expense queries",
		"an agent people can converse with about benefits",
		"a conversational agent for travel",
		"an agent that answers questions on demand",
	}
	for _, s := range interactive {
		if !ConversationalIntent(s) {
			t.Errorf("not recognised as interactive: %q", s)
		}
	}
}

// The widened vocabulary must not swallow genuine pipelines — that would push
// scheduled work onto a reasoning loop, which is the opposite failure.
func TestConversationalIntentStillRejectsPipelines(t *testing.T) {
	pipelines := []string{
		"Every weekday at 7am send a digest of AI research news to telegram",
		"Each morning fetch the top stories and email a summary",
		"Nightly, back up the database and report the result to slack",
		"Every hour poll the sensor feed and alert on faults",
		"On a schedule, ingest new URLs into the knowledge base",
	}
	for _, s := range pipelines {
		if ConversationalIntent(s) {
			t.Errorf("a scheduled pipeline was treated as interactive: %q", s)
		}
	}
}

// The reported prompt, end to end: it must not be told its own request is
// unintelligible, and it must not be described as manually triggered.
func TestConversationAgentSpecIsUsable(t *testing.T) {
	const intent = "I want to develop a conversation agent that provides timely " +
		"weather updates based on a place or a zipcode"

	spec := ExtractBuildSpecFrom(intent, Catalog{Tools: []string{"web_search"}})

	for _, q := range spec.Blockers() {
		if q.ID == "stages" {
			t.Errorf("blocked with %q — but the prompt says plainly what the agent does", q.Why)
		}
	}
	if !spec.Ready() {
		t.Errorf("spec not ready; blockers: %+v", spec.Blockers())
	}
	if spec.Trigger != "channel" {
		t.Errorf("trigger = %q, want channel: a conversation agent is started by a message, "+
			"not by someone pressing Run", spec.Trigger)
	}
}

// A vague request with no capability and no interactive framing must STILL
// block — widening the exemption must not disable the check entirely.
func TestVagueNonConversationalIntentStillBlocks(t *testing.T) {
	spec := ExtractBuildSpecFrom("something helpful for the team", Catalog{})
	var found bool
	for _, q := range spec.Blockers() {
		if q.ID == "stages" {
			found = true
		}
	}
	if !found {
		t.Error("a vague intent with no capability and no interactive framing should still block")
	}
}
