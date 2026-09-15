package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/person"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestCooldownStopsABurstOfObservationsBecomingABurstOfRuns(t *testing.T) {
	personTriggerCooldowns.Lock()
	personTriggerCooldowns.last = map[string]time.Time{}
	personTriggerCooldowns.Unlock()

	now := time.Now().UTC()
	// A phone that has been offline delivers many observations at once.
	if !claimPersonTrigger("steward", "kai", time.Hour, now) {
		t.Fatal("the first change should run the agent")
	}
	for i := 1; i <= 20; i++ {
		if claimPersonTrigger("steward", "kai", time.Hour, now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("observation %d ran the agent again inside the cooldown", i)
		}
	}
	// Once the cooldown has passed, it may run again.
	if !claimPersonTrigger("steward", "kai", time.Hour, now.Add(61*time.Minute)) {
		t.Fatal("the agent should run again after its cooldown")
	}
}

func TestCooldownIsPerPersonNotPerAgent(t *testing.T) {
	personTriggerCooldowns.Lock()
	personTriggerCooldowns.last = map[string]time.Time{}
	personTriggerCooldowns.Unlock()

	now := time.Now().UTC()
	if !claimPersonTrigger("steward", "kai", time.Hour, now) {
		t.Fatal("kai's first change should run")
	}
	// Priya's day is not Kai's. One household must not silence the other.
	if !claimPersonTrigger("steward", "priya-s", time.Hour, now) {
		t.Fatal("another member's change must not be swallowed by the first one's cooldown")
	}
}

func TestAZeroCooldownIsHonoured(t *testing.T) {
	personTriggerCooldowns.Lock()
	personTriggerCooldowns.last = map[string]time.Time{}
	personTriggerCooldowns.Unlock()

	now := time.Now().UTC()
	// Two separate claims, because each one records a run: an explicit zero
	// cooldown means the second is allowed immediately.
	first := claimPersonTrigger("noisy", "kai", 0, now)
	second := claimPersonTrigger("noisy", "kai", 0, now)
	if !first || !second {
		t.Fatalf("an explicit zero cooldown means no rate limit: %v %v", first, second)
	}
}

func TestTheAgentIsToldWhyItWasWoken(t *testing.T) {
	def := &agent.Definition{ID: "steward", Name: "Steward", Trigger: agent.TriggerPerson,
		Person: &agent.PersonTrigger{When: "commitment.due"}}
	fires := []person.Fire{
		{When: "commitment.due", Reason: "A commitment is coming up: Send Priya the proposal (due tomorrow)."},
		{When: "commitment.due", Reason: "A commitment is coming up: Pay the water bill (overdue since 12 Sep)."},
	}

	msg := personTriggerMessage(def, fires, "kai")

	text := ""
	for _, part := range msg.Parts {
		text += part.Text
	}
	if !strings.Contains(text, "Send Priya the proposal") || !strings.Contains(text, "Pay the water bill") {
		t.Fatalf("every reason should reach the agent, not just the first: %q", text)
	}
	if !strings.Contains(text, "say nothing if it is not") {
		t.Fatalf("the agent must be told silence is allowed: %q", text)
	}
	if msg.UserID != "kai" || msg.Metadata["person.owner"] != "kai" {
		t.Fatalf("the run must be attributed to the person: %+v", msg.Metadata)
	}
	if msg.Metadata["trigger"] != "person" || msg.Metadata["person.when"] != "commitment.due" {
		t.Fatalf("metadata: %+v", msg.Metadata)
	}
	if msg.AgentID != "steward" || msg.SessionID == "" {
		t.Fatalf("message identity: %+v", msg)
	}
}

func TestOnlyEnabledPersonAgentsWithUsableDurationsAreWatched(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dir := s.cfg.AgentDirs[0]

	write := func(id string, def *agent.Definition) {
		t.Helper()
		def.ID = id
		def.Name = id
		if err := s.loader.Upsert(dir, def); err != nil {
			t.Fatal(err)
		}
	}
	write("good", &agent.Definition{Enabled: true, Trigger: agent.TriggerPerson,
		Person: &agent.PersonTrigger{When: "state.changed", Cooldown: "30m"},
		LLM:    agent.LLMConfig{Provider: "test", Model: "m"}})
	write("disabled", &agent.Definition{Enabled: false, Trigger: agent.TriggerPerson,
		Person: &agent.PersonTrigger{When: "state.changed"},
		LLM:    agent.LLMConfig{Provider: "test", Model: "m"}})
	write("bad-window", &agent.Definition{Enabled: true, Trigger: agent.TriggerPerson,
		Person: &agent.PersonTrigger{When: "commitment.due", Within: "2 hours"},
		LLM:    agent.LLMConfig{Provider: "test", Model: "m"}})
	write("not-a-person-agent", &agent.Definition{Enabled: true, Trigger: agent.TriggerChannel,
		LLM: agent.LLMConfig{Provider: "test", Model: "m"}})

	watched := map[string]bool{}
	for _, def := range s.personTriggerAgents() {
		watched[def.ID] = true
	}
	if !watched["good"] {
		t.Fatal("an enabled person agent should be watched")
	}
	for _, id := range []string{"disabled", "bad-window", "not-a-person-agent"} {
		if watched[id] {
			t.Fatalf("%q should not be watched", id)
		}
	}
}
