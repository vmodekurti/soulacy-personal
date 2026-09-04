package studio

import "testing"

func chanCat() Catalog { return Catalog{Channels: []string{"telegram", "slack", "email"}} }

// Studio used to pick the delivery channel itself when the intent named none —
// in practice always the first configured channel, so every generated agent
// delivered to the same place whether or not that was wanted. It must ask.
func TestSpecAsksWhichChannelWhenNoneNamed(t *testing.T) {
	spec := ExtractBuildSpecFrom("every weekday summarise the top stories and send me the digest", chanCat())
	var q *SpecQuestion
	for i := range spec.Questions {
		if spec.Questions[i].ID == "output_channel" {
			q = &spec.Questions[i]
		}
	}
	if q == nil {
		t.Fatalf("no output_channel question; questions = %+v", spec.Questions)
	}
	if !q.Blocker {
		t.Error("the channel question must block generation — otherwise Studio picks for you again")
	}
	if len(q.Options) == 0 {
		t.Fatal("the question offers no channels to choose from")
	}
	// Only installed channels, plus an explicit "nowhere".
	var hasNone bool
	for _, o := range q.Options {
		if o == NoDeliveryChannel {
			hasNone = true
		}
	}
	if !hasNone {
		t.Error(`"none" must be offered — "returns its answer to whoever asked" is a valid design`)
	}
	if spec.Ready() {
		t.Error("spec reports ready while the channel is still unanswered")
	}
}

// Naming the channel is answering the question — don't interrogate.
func TestSpecDoesNotAskWhenTheIntentNamesTheChannel(t *testing.T) {
	spec := ExtractBuildSpecFrom("every weekday summarise the top stories and send the digest to slack", chanCat())
	for _, q := range spec.Questions {
		if q.ID == "output_channel" {
			t.Errorf("asked which channel even though the intent said slack: %+v", q)
		}
	}
}

func TestSpecAsksWhichChannelItListensOn(t *testing.T) {
	spec := ExtractBuildSpecFrom("answer questions when someone messages me", chanCat())
	if spec.Trigger != "channel" {
		t.Skipf("intent did not read as channel-triggered (trigger=%q)", spec.Trigger)
	}
	var found bool
	for _, q := range spec.Questions {
		if q.ID == "input_channel" {
			found = true
			if !q.Blocker || len(q.Options) == 0 {
				t.Errorf("input_channel question is not a usable picker: %+v", q)
			}
		}
	}
	if !found {
		t.Errorf("no input_channel question; questions = %+v", spec.Questions)
	}
}

// The answer must be APPLIED, not merely shown to the model.
func TestChannelAnswerOverridesWhateverTheModelChose(t *testing.T) {
	d := Draft{Channels: []string{"telegram"}}
	ApplyChannelAnswers(&d, map[string]string{"output_channel": "slack"})
	if len(d.Channels) != 1 || d.Channels[0] != "slack" {
		t.Errorf("channels = %v, want [slack]", d.Channels)
	}
}

func TestNoneClearsDelivery(t *testing.T) {
	d := Draft{Channels: []string{"telegram"}}
	ApplyChannelAnswers(&d, map[string]string{"output_channel": NoDeliveryChannel})
	if len(d.Channels) != 0 {
		t.Errorf("channels = %v, want empty when the operator chose none", d.Channels)
	}
}

func TestInputChannelIsAddedNotSubstituted(t *testing.T) {
	d := Draft{}
	ApplyChannelAnswers(&d, map[string]string{"output_channel": "email", "input_channel": "slack"})
	if len(d.Channels) != 2 || d.Channels[0] != "email" || d.Channels[1] != "slack" {
		t.Errorf("channels = %v, want [email slack]", d.Channels)
	}
}

// The prompt used to show the model `"channels": ["telegram"]` as the example
// answer, which is most of why it always said telegram.
func TestGenerationPromptsDoNotNameAChannelAsTheExample(t *testing.T) {
	for name, prompt := range map[string]string{
		"agent":    BuildAgentPrompt("do a thing", chanCat(), "auto", nil),
		"workflow": BuildPrompt("do a thing", chanCat(), nil),
	} {
		if containsFold([]string{prompt}, "") { // keep containsFold referenced if unused elsewhere
			_ = name
		}
		for _, banned := range []string{`"channels": ["telegram"]`, `"channels":["telegram"]`} {
			if idx := indexOf(prompt, banned); idx >= 0 {
				t.Errorf("%s prompt hands the model %s as the example answer", name, banned)
			}
		}
	}
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
